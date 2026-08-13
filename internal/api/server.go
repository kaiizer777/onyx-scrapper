package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/agent"

	"github.com/kaiizer777/onyx-scrapper/internal/crawl"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/extract"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/profile"
	"github.com/kaiizer777/onyx-scrapper/internal/research"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/internal/webui"
)

// Server represents the Onyx Scrapper local HTTP API server.
type Server struct {
	port      int
	client    *llm.Client
	store     *store.Store
	registry  *discovery.Registry
	httpSrv   *http.Server

	// telegramWebhook is an optional http.Handler mounted at
	// /telegram/webhook when the gateway is in webhook mode. It is
	// set via WithTelegramWebhook and registered into the mux inside
	// NewServer (not in the option itself, because the mux does not
	// exist yet at option time).
	telegramWebhook http.Handler

	activeRunsMu sync.Mutex
	activeRuns   map[string]context.CancelFunc
}

// Option configures Server options.
type Option func(*Server)

// WithPort sets the HTTP server listening port.
func WithPort(port int) Option {
	return func(s *Server) {
		if port > 0 {
			s.port = port
		}
	}
}

// WithLLMClient sets the LLM client instance.
func WithLLMClient(client *llm.Client) Option {
	return func(s *Server) {
		s.client = client
	}
}

// WithStore sets the SQLite store instance.
func WithStore(st *store.Store) Option {
	return func(s *Server) {
		s.store = st
	}
}

// WithRegistry sets the registry instance.
func WithRegistry(registry *discovery.Registry) Option {
	return func(s *Server) {
		s.registry = registry
	}
}

// telegramWebhookPath is the path the Telegram gateway is mounted at
// when the operator runs the bot in webhook mode on the same process
// as onyx serve. Centralized so the option below and any future
// docs/tests reference the same constant.
const telegramWebhookPath = "/telegram/webhook"

// WithTelegramWebhook mounts an http.Handler at /telegram/webhook on
// the existing onyx serve mux. This lets a single onyx process run
// the HTTP API + the Telegram webhook gateway together, sharing the
// same TLS-terminating reverse proxy in front. Nil handler is a
// no-op so callers can pass a conditional without a separate check.
func WithTelegramWebhook(handler http.Handler) Option {
	return func(s *Server) {
		if handler == nil {
			return
		}
		// Captured into a field so NewServer can register it after the
		// mux is built (the mux does not exist when the option runs).
		s.telegramWebhook = handler
	}
}

// NewServer constructs a new HTTP API server.
func NewServer(opts ...Option) *Server {
	s := &Server{
		port:       9090,
		activeRuns: make(map[string]context.CancelFunc),
	}
	for _, opt := range opts {
		opt(s)
	}

	// We no longer initialize a default search service here, caller must provide registry.

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", s.corsMiddleware(s.handlePing))
	mux.HandleFunc("/health", s.corsMiddleware(s.handlePing))
	mux.HandleFunc("/search", s.corsMiddleware(s.handleSearch))
	mux.HandleFunc("/fetch", s.corsMiddleware(s.handleFetch))
	mux.HandleFunc("/extract", s.corsMiddleware(s.handleExtract))
	mux.HandleFunc("/agent", s.corsMiddleware(s.handleAgent))
	mux.HandleFunc("/agent/async", s.corsMiddleware(s.handleAgentAsync))
	mux.HandleFunc("/agent/runs", s.corsMiddleware(s.handleAgentRuns))
	mux.HandleFunc("/agent/runs/{id}", s.corsMiddleware(s.handleAgentRunDetail))
	mux.HandleFunc("/crawl", s.corsMiddleware(s.handleCrawl))
	mux.HandleFunc("/deep-research", s.corsMiddleware(s.handleDeepResearch))
	mux.HandleFunc("/deep-research/{id}", s.corsMiddleware(s.handleDeepResearchDetail))
	mux.HandleFunc("POST /agent/runs/{id}/cancel", s.corsMiddleware(s.handleCancelRun))
	mux.HandleFunc("POST /deep-research/{id}/cancel", s.corsMiddleware(s.handleCancelRun))

	mux.HandleFunc("GET /health/searx", s.corsMiddleware(s.handleSearxHealth))
	mux.HandleFunc("GET /profile", s.corsMiddleware(s.handleGetProfile))
	mux.HandleFunc("POST /profile", s.corsMiddleware(s.handlePostProfile))

	uiHandler, err := webui.NewUIHandler(s.store, s.client, s.registry)
	if err == nil {
		uiHandler.RegisterRoutes(mux)
	} else {
		slog.Warn("Failed to initialize Web UI handler", "error", err)
	}

	// Telegram webhook (opt-in). Mounted AFTER the WebUI routes so
	// /telegram/webhook is a stable path operators can front with
	// their reverse proxy without colliding with WebUI prefixes.
	if s.telegramWebhook != nil {
		mux.Handle(telegramWebhookPath, s.telegramWebhook)
		slog.Info("Telegram webhook mounted on serve mux", "path", telegramWebhookPath)
	}

	// Wrap mux: serve ui.html at root without registering "GET /" in the mux
	// ("GET /" conflicts with method-less patterns like /health in Go 1.22+ ServeMux)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && r.Method == http.MethodGet {
			s.handleUI(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})

	s.httpSrv = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      handler,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

// Start runs the HTTP server. It blocks until Shutdown or ListenAndServe returns an error.
func (s *Server) Start() error {
	slog.Info("Onyx API Server starting", "port", s.port, "url", fmt.Sprintf("http://localhost:%d", s.port))
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// Port returns the server's configured port.
func (s *Server) Port() int {
	return s.port
}

func (s *Server) corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "ok",
		"service":   "onyx-scrapper",
		"port":      s.port,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

type searchRequest struct {
	Q     string `json:"q"`
	Query string `json:"query"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var queryStr string

	if r.Method == http.MethodGet {
		queryStr = r.URL.Query().Get("q")
		if queryStr == "" {
			queryStr = r.URL.Query().Get("query")
		}
	} else if r.Method == http.MethodPost {
		var req searchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			queryStr = req.Q
			if queryStr == "" {
				queryStr = req.Query
			}
		}
		if queryStr == "" {
			queryStr = r.URL.Query().Get("q")
		}
	}

	queryStr = strings.TrimSpace(queryStr)
	res := s.registry.Search(r.Context(), queryStr)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"query":   queryStr,
		"results": res,
	})
}

type fetchRequest struct {
	URL    string `json:"url"`
	Render bool   `json:"render"`
}

type fetchResponse struct {
	URL         string `json:"url"`
	UsedBrowser bool   `json:"used_browser"`
	CleanText   string `json:"clean_text"`
	RawHTML     string `json:"raw_html,omitempty"`
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	var targetURL string
	var forceRender bool

	if r.Method == http.MethodGet {
		targetURL = r.URL.Query().Get("url")
		forceRender, _ = strconv.ParseBool(r.URL.Query().Get("render"))
	} else if r.Method == http.MethodPost {
		var req fetchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.URL.Query().Get("url") == "" {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		targetURL = req.URL
		forceRender = req.Render
		if targetURL == "" {
			targetURL = r.URL.Query().Get("url")
		}
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		writeError(w, http.StatusBadRequest, "parameter 'url' is required")
		return
	}

	rawHTML, usedBrowser, err := extract.Fetch(r.Context(), targetURL, forceRender)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("fetch failed: %v", err))
		return
	}

	cleanText, cleanErr := extract.CleanHTML(rawHTML)
	if cleanErr != nil {
		cleanText = rawHTML
	}

	if s.store != nil {
		if _, saveErr := s.store.SavePage(targetURL, rawHTML, cleanText, "api", "ok"); saveErr != nil {
			slog.Warn("Failed to save fetched page to store", "url", targetURL, "error", saveErr)
		}
	}

	writeJSON(w, http.StatusOK, fetchResponse{
		URL:         targetURL,
		UsedBrowser: usedBrowser,
		CleanText:   cleanText,
		RawHTML:     rawHTML,
	})
}

type extractRequest struct {
	URL    string `json:"url"`
	Schema string `json:"schema"`
	Render bool   `json:"render"`
}

type extractResponse struct {
	URL    string          `json:"url"`
	Schema string          `json:"schema"`
	Data   json.RawMessage `json:"data"`
}

func (s *Server) handleExtract(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}

	var req extractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "field 'url' is required")
		return
	}

	if req.Schema == "" {
		req.Schema = "product"
	}

	if s.client == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM client is not configured on server")
		return
	}

	rawHTML, _, err := extract.Fetch(r.Context(), req.URL, req.Render)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("fetch failed during extraction: %v", err))
		return
	}

	rawJSON, err := extract.ExtractJSON(r.Context(), s.client, rawHTML, req.Schema)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("extraction failed: %v", err))
		return
	}

	if s.store != nil {
		cleanText, _ := extract.CleanHTML(rawHTML)
		if pageID, saveErr := s.store.SavePage(req.URL, rawHTML, cleanText, "api", "ok"); saveErr == nil {
			_, _ = s.store.SaveExtraction(pageID, req.Schema, string(rawJSON))
		}
	}

	writeJSON(w, http.StatusOK, extractResponse{
		URL:    req.URL,
		Schema: req.Schema,
		Data:   rawJSON,
	})
}

type agentRequest struct {
	Goal     string `json:"goal"`
	MaxSteps int    `json:"max_steps"`
}

type agentResponse struct {
	RunID      int64  `json:"run_id"`
	Status     string `json:"status"`
	Result     string `json:"result"`
	StepsCount int    `json:"steps_count,omitempty"`
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}

	var req agentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.Goal = strings.TrimSpace(req.Goal)
	if req.Goal == "" {
		writeError(w, http.StatusBadRequest, "field 'goal' is required")
		return
	}

	// max_steps is no longer user-configurable; always use the hard cap (agent.DefaultMaxSteps = 40).

	if s.client == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM client or Store is not configured on server")
		return
	}

	agentOpts := []agent.Option{agent.WithRegistry(s.registry)}
	if newsCtx := s.loadNewsContext(req.Goal); newsCtx != "" {
		agentOpts = append(agentOpts, agent.WithNewsContext(newsCtx))
	}

	ag := agent.NewAgent(s.client, s.store, agentOpts...)
	run, err := ag.Run(r.Context(), req.Goal, 0, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("agent execution failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, agentResponse{
		RunID:  run.ID,
		Status: run.Status,
		Result: run.Result,
	})
}

func (s *Server) handleAgentAsync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}

	var req agentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.Goal = strings.TrimSpace(req.Goal)
	if req.Goal == "" {
		writeError(w, http.StatusBadRequest, "field 'goal' is required")
		return
	}
	// max_steps is no longer user-configurable; always use the hard cap (agent.DefaultMaxSteps = 40).
	if s.client == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM client or Store is not configured on server")
		return
	}

	agentOpts := []agent.Option{agent.WithRegistry(s.registry)}
	if newsCtx := s.loadNewsContext(req.Goal); newsCtx != "" {
		agentOpts = append(agentOpts, agent.WithNewsContext(newsCtx))
	}

	ag := agent.NewAgent(s.client, s.store, agentOpts...)
	runID, err := s.store.CreateAgentRun(req.Goal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create agent run: %v", err))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	runKey := fmt.Sprintf("agent_%d", runID)
	s.activeRunsMu.Lock()
	s.activeRuns[runKey] = cancel
	s.activeRunsMu.Unlock()

	go func() {
		defer func() {
			s.activeRunsMu.Lock()
			delete(s.activeRuns, runKey)
			s.activeRunsMu.Unlock()
		}()
		_, _ = ag.Run(ctx, req.Goal, runID, nil)
	}()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"run_id": runID,
		"status": "running",
	})
}

func (s *Server) handleAgentRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use GET")
		return
	}

	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store not configured")
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := s.store.GetAgentRuns(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list runs: %v", err))
		return
	}
	if runs == nil {
		runs = []store.AgentRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

type agentRunDetail struct {
	Run   store.AgentRun    `json:"run"`
	Steps []store.AgentStep `json:"steps"`
}

func (s *Server) handleAgentRunDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use GET")
		return
	}

	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store not configured")
		return
	}
	idStr := r.PathValue("id")
	if idStr == "" {
		writeError(w, http.StatusBadRequest, "run id required")
		return
	}
	runID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	run, err := s.store.GetAgentRun(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get run: %v", err))
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	steps, err := s.store.GetAgentSteps(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get steps: %v", err))
		return
	}
	if steps == nil {
		steps = []store.AgentStep{}
	}
	writeJSON(w, http.StatusOK, agentRunDetail{
		Run:   *run,
		Steps: steps,
	})
}

func (s *Server) handleSearxHealth(w http.ResponseWriter, r *http.Request) {
	baseURL := os.Getenv("SEARXNG_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	searxURL := strings.TrimRight(baseURL, "/") + "/search?q=test&format=json"
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(searxURL)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "down", "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "up", "code": resp.StatusCode})
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	uiPath := "ui.html"
	if _, err := os.Stat(uiPath); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "ui.html not found in project root")
		return
	}
	http.ServeFile(w, r, uiPath)
}

type crawlRequest struct {
	URL      string `json:"url"`
	StartURL string `json:"start_url"`
	MaxPages int    `json:"max_pages"`
	MaxDepth int    `json:"max_depth"`
	Workers  int    `json:"workers"`
	Render   bool   `json:"render"`
	Schema   string `json:"schema"`
}

func (s *Server) handleCrawl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req crawlRequest

	if r.Method == http.MethodGet {
		req.URL = r.URL.Query().Get("url")
		if req.URL == "" {
			req.URL = r.URL.Query().Get("start_url")
		}
		if mp, err := strconv.Atoi(r.URL.Query().Get("max_pages")); err == nil && mp > 0 {
			req.MaxPages = mp
		}
		if md, err := strconv.Atoi(r.URL.Query().Get("max_depth")); err == nil && md > 0 {
			req.MaxDepth = md
		}
		if wCount, err := strconv.Atoi(r.URL.Query().Get("workers")); err == nil && wCount > 0 {
			req.Workers = wCount
		}
		req.Render, _ = strconv.ParseBool(r.URL.Query().Get("render"))
		req.Schema = r.URL.Query().Get("schema")
	} else if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.URL.Query().Get("url") == "" {
			writeError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		if req.URL == "" {
			req.URL = req.StartURL
		}
		if req.URL == "" {
			req.URL = r.URL.Query().Get("url")
		}
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "parameter 'url' or 'start_url' is required")
		return
	}

	if req.MaxPages <= 0 {
		req.MaxPages = 50
	}
	if req.MaxDepth <= 0 {
		req.MaxDepth = 3
	}
	if req.Workers <= 0 {
		req.Workers = 5
	}

	crawler := crawl.NewCrawler()
	res, err := crawler.Crawl(r.Context(), crawl.CrawlOptions{
		StartURL:  req.URL,
		MaxPages:  req.MaxPages,
		MaxDepth:  req.MaxDepth,
		Workers:   req.Workers,
		Render:    req.Render,
		Schema:    req.Schema,
		Store:     s.store,
		LLMClient: s.client,
	})

	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("crawl failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, res)
}

type deepResearchRequest struct {
	Query        string `json:"query"`
	MaxQuestions int    `json:"max_questions"`
}

func (s *Server) handleDeepResearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}

	var req deepResearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "field 'query' is required")
		return
	}


	if s.client == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM client or Store is not configured on server")
		return
	}

	// Augment the research goal with profile news context when applicable.
	researchGoal := req.Query
	if newsCtx := s.loadNewsContext(req.Query); newsCtx != "" {
		researchGoal = req.Query + "\n\n" + newsCtx
	}

	orchestrator := research.NewOrchestrator(s.client, s.store, s.registry, nil)

	// Create run ID
	runID, err := s.store.CreateResearchRun(req.Query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create run: %v", err))
		return
	}

	// MaxSubQuestions is omitted — the orchestrator enforces a hard cap of 40 internally.
	// AI controls research depth; the cap only exists to prevent runaway infinite loops.
	opts := research.Options{
		ResumeRunID: runID,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runKey := fmt.Sprintf("research_%d", runID)
	s.activeRunsMu.Lock()
	s.activeRuns[runKey] = cancel
	s.activeRunsMu.Unlock()

	go func() {
		defer func() {
			s.activeRunsMu.Lock()
			delete(s.activeRuns, runKey)
			s.activeRunsMu.Unlock()
		}()
		_, _ = orchestrator.Run(ctx, researchGoal, opts)
	}()

	w.WriteHeader(http.StatusAccepted) // 202
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"run_id": runID,
		"status": "running",
	})
}

func (s *Server) handleDeepResearchDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use GET")
		return
	}

	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store not configured")
		return
	}

	idStr := r.PathValue("id")
	if idStr == "" {
		writeError(w, http.StatusBadRequest, "run id required")
		return
	}

	runID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}

	run, err := s.store.GetResearchRun(runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get run: %v", err))
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	sqs, _ := s.store.GetSubQuestionsForRun(runID)
	if sqs == nil {
		sqs = []store.ResearchSubQuestion{}
	}
	
	findings, _ := s.store.GetAllFindingsForRun(runID)
	if findings == nil {
		findings = []store.Finding{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"run":           run,
		"sub_questions": sqs,
		"findings":      findings,
	})
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}

	runType := ""
	if strings.HasPrefix(r.URL.Path, "/agent/") {
		runType = "agent"
	} else if strings.HasPrefix(r.URL.Path, "/deep-research/") {
		runType = "research"
	} else {
		writeError(w, http.StatusBadRequest, "unknown run type")
		return
	}

	idStr := r.PathValue("id")
	if idStr == "" {
		writeError(w, http.StatusBadRequest, "run id required")
		return
	}

	runID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid run id")
		return
	}

	runKey := fmt.Sprintf("%s_%d", runType, runID)
	s.activeRunsMu.Lock()
	cancel, exists := s.activeRuns[runKey]
	if exists {
		cancel()
		delete(s.activeRuns, runKey)
	}
	s.activeRunsMu.Unlock()

	switch runType {
	case "agent":
		_ = s.store.UpdateAgentRunStatus(runID, "cancelled", "Run cancelled by user")
	case "research":
		_ = s.store.UpdateResearchRunStatus(runID, "cancelled", "Run cancelled by user")
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}



func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use GET")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store is not configured")
		return
	}

	mgr := profile.NewManager(s.store, profile.Config{})
	prof, err := mgr.GetOrCreateDefaultProfile()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get default profile: %v", err))
		return
	}

	pwf, err := mgr.GetProfileWithFields(prof.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to load profile fields: %v", err))
		return
	}

	if pwf.Fields == nil {
		pwf.Fields = []store.ProfileField{}
	}

	writeJSON(w, http.StatusOK, pwf)
}

type postProfileRequest struct {
	Fields []store.ProfileField `json:"fields"`
}

func (s *Server) handlePostProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed, use POST")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store is not configured")
		return
	}

	var req postProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	mgr := profile.NewManager(s.store, profile.Config{})
	prof, err := mgr.GetOrCreateDefaultProfile()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get default profile: %v", err))
		return
	}

	syncedFields, err := mgr.SyncFields(prof.ID, req.Fields)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, profile.ProfileWithFields{
		Profile: prof,
		Fields:  syncedFields,
	})
}

// loadNewsContext checks whether query is news-related and, if so, loads the
// default user profile's enabled interest fields and returns a formatted
// instruction string for injection into LLM system prompts.
// Returns "" when the query is not news-related or the profile has no enabled fields.
func (s *Server) loadNewsContext(query string) string {
	if s.store == nil {
		return ""
	}
	if !profile.IsNewsQuery(query) {
		return ""
	}
	mgr := profile.NewManager(s.store, profile.Config{})
	prof, err := mgr.GetOrCreateDefaultProfile()
	if err != nil {
		slog.Warn("loadNewsContext: failed to get default profile", "error", err)
		return ""
	}
	fields, err := s.store.ListEnabledProfileFields(prof.ID)
	if err != nil {
		slog.Warn("loadNewsContext: failed to list enabled profile fields", "error", err)
		return ""
	}
	ctx, ok := profile.BuildNewsContext(fields)
	if !ok {
		return ""
	}
	slog.Info("News context injected from user profile", "topics", len(fields))
	return ctx
}
