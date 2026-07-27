package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaiizer-99/onyx-scrapper/internal/agent"
	"github.com/kaiizer-99/onyx-scrapper/internal/crawl"
	"github.com/kaiizer-99/onyx-scrapper/internal/extract"
	"github.com/kaiizer-99/onyx-scrapper/internal/llm"
	"github.com/kaiizer-99/onyx-scrapper/internal/search"
	"github.com/kaiizer-99/onyx-scrapper/internal/store"
)

// Server represents the Onyx Scrapper local HTTP API server.
type Server struct {
	port      int
	client    *llm.Client
	store     *store.Store
	searchSvc *search.Service
	httpSrv   *http.Server
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

// WithSearchService sets the search service instance.
func WithSearchService(svc *search.Service) Option {
	return func(s *Server) {
		s.searchSvc = svc
	}
}

// NewServer constructs a new HTTP API server.
func NewServer(opts ...Option) *Server {
	s := &Server{
		port: 9090,
	}
	for _, opt := range opts {
		opt(s)
	}

	if s.searchSvc == nil {
		s.searchSvc = search.NewService(s.store)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", s.handlePing)
	mux.HandleFunc("/health", s.handlePing)
	mux.HandleFunc("/search", s.corsMiddleware(s.handleSearch))
	mux.HandleFunc("/fetch", s.corsMiddleware(s.handleFetch))
	mux.HandleFunc("/extract", s.corsMiddleware(s.handleExtract))
	mux.HandleFunc("/agent", s.corsMiddleware(s.handleAgent))
	mux.HandleFunc("/crawl", s.corsMiddleware(s.handleCrawl))

	s.httpSrv = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
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
	res, err := s.searchSvc.Search(r.Context(), queryStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("search failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, res)
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
		if _, saveErr := s.store.SavePage(targetURL, rawHTML, cleanText); saveErr != nil {
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
		if pageID, saveErr := s.store.SavePage(req.URL, rawHTML, cleanText); saveErr == nil {
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

	if req.MaxSteps <= 0 {
		req.MaxSteps = 15
	}

	if s.client == nil || s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM client or Store is not configured on server")
		return
	}

	ag := agent.NewAgent(s.client, s.store, agent.WithMaxSteps(req.MaxSteps), agent.WithSearchService(s.searchSvc))
	run, err := ag.Run(r.Context(), req.Goal, nil)
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

