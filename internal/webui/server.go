package webui

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/agent"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/research"
	"github.com/kaiizer777/onyx-scrapper/internal/search"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

//go:embed templates/*.html
var templatesFS embed.FS

// UIHandler manages the web dashboard UI
type UIHandler struct {
	store     *store.Store
	client    *llm.Client
	searchSvc *search.Service
	tmpl      *template.Template
	md        goldmark.Markdown
}

// NewUIHandler creates a new handler for the web UI.
func NewUIHandler(st *store.Store, client *llm.Client, searchSvc *search.Service) (*UIHandler, error) {
	// Parse templates
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithHardWraps(), html.WithUnsafe()),
	)

	return &UIHandler{
		store:     st,
		client:    client,
		searchSvc: searchSvc,
		tmpl:      tmpl,
		md:        md,
	}, nil
}

// RegisterRoutes mounts the UI routes onto a mux
func (h *UIHandler) RegisterRoutes(mux *http.ServeMux) {
	// Security: This UI is intentionally unauthenticated and bound to localhost for local dev.
	// Never expose this directly to the internet without adding authentication.
	
	mux.HandleFunc("GET /ui", h.handleDashboard)
	
	mux.HandleFunc("GET /ui/pages", h.handlePages)
	mux.HandleFunc("GET /ui/page/{id}", h.handlePageDetail)
	
	mux.HandleFunc("GET /ui/agent", h.handleAgentList)
	mux.HandleFunc("POST /ui/agent", h.handleAgentLaunch)
	mux.HandleFunc("GET /ui/agent/{id}", h.handleAgentDetail)
	mux.HandleFunc("GET /ui/agent/{id}/status", h.handleAgentStatus)
	
	mux.HandleFunc("GET /ui/research", h.handleResearchList)
	mux.HandleFunc("POST /ui/research", h.handleResearchLaunch)
	mux.HandleFunc("GET /ui/research/{id}", h.handleResearchDetail)
	mux.HandleFunc("GET /ui/research/{id}/status", h.handleResearchStatus)
}

func (h *UIHandler) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := h.tmpl.ExecuteTemplate(w, name, data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template rendering error: %v", err), http.StatusInternalServerError)
	}
}

func (h *UIHandler) renderPartial(w http.ResponseWriter, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// If the template needs the base layout, but we only want to render a specific block or partial:
	err := h.tmpl.ExecuteTemplate(w, name, data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Partial rendering error: %v", err), http.StatusInternalServerError)
	}
}

// --- Dashboard ---
func (h *UIHandler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	stats, _ := h.store.GetStats()
	h.renderTemplate(w, "base.html", map[string]interface{}{
		"ActiveNav": "dashboard",
		"Stats":     stats,
	})
}

// --- Pages ---
func (h *UIHandler) handlePages(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	
	var pages []store.Page
	var searchResults []store.SearchResult
	var isSearch bool
	
	if query != "" {
		isSearch = true
		searchResults, _ = h.store.SearchPages(query)
	} else {
		pages, _ = h.store.GetRecentPages(50, 0)
	}

	data := map[string]interface{}{
		"ActiveNav": "pages",
		"Query":     query,
		"IsSearch":  isSearch,
	}
	
	if isSearch {
		data["Pages"] = searchResults
	} else {
		data["Pages"] = pages
	}

	h.renderTemplate(w, "base.html", data)
}

func (h *UIHandler) handlePageDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	pageID, _ := strconv.ParseInt(idStr, 10, 64)
	
	page, _ := h.store.GetPageByID(pageID)
	if page == nil {
		http.NotFound(w, r)
		return
	}
	
	extractions, _ := h.store.GetExtractionsForPage(pageID)

	h.renderTemplate(w, "base.html", map[string]interface{}{
		"ActiveNav": "pages",
		"Page":      page,
		"Extractions": extractions,
	})
}

// --- Agent ---
func (h *UIHandler) handleAgentList(w http.ResponseWriter, r *http.Request) {
	runs, _ := h.store.GetAgentRuns(50)
	h.renderTemplate(w, "base.html", map[string]interface{}{
		"ActiveNav": "agent",
		"Runs":      runs,
	})
}

func (h *UIHandler) handleAgentLaunch(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	
	goal := strings.TrimSpace(r.FormValue("goal"))
	if goal == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}
	
	maxSteps, _ := strconv.Atoi(r.FormValue("max_steps"))
	if maxSteps <= 0 {
		maxSteps = 15
	}
	
	runID, err := h.store.CreateAgentRun(goal)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create run: %v", err), http.StatusInternalServerError)
		return
	}
	
	ag := agent.NewAgent(h.client, h.store, agent.WithMaxSteps(maxSteps), agent.WithSearchService(h.searchSvc))
	
	go func() {
		_, _ = ag.Run(context.Background(), goal, runID, nil)
	}()
	
	http.Redirect(w, r, fmt.Sprintf("/ui/agent/%d", runID), http.StatusSeeOther)
}

func (h *UIHandler) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	runID, _ := strconv.ParseInt(idStr, 10, 64)
	
	run, _ := h.store.GetAgentRun(runID)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	
	steps, _ := h.store.GetAgentSteps(runID)
	
	h.renderTemplate(w, "base.html", map[string]interface{}{
		"ActiveNav": "agent",
		"RunDetail": map[string]interface{}{
			"Run":   run,
			"Steps": steps,
		},
	})
}

func (h *UIHandler) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	runID, _ := strconv.ParseInt(idStr, 10, 64)
	
	run, _ := h.store.GetAgentRun(runID)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	
	steps, _ := h.store.GetAgentSteps(runID)
	
	h.renderPartial(w, "_run_status.html", map[string]interface{}{
		"Type":   "agent",
		"Status": run.Status,
		"Result": run.Result,
		"Steps":  steps,
	})
}

// --- Research ---
func (h *UIHandler) handleResearchList(w http.ResponseWriter, r *http.Request) {
	runs, _ := h.store.GetRecentResearchRuns(50)
	h.renderTemplate(w, "base.html", map[string]interface{}{
		"ActiveNav": "research",
		"Runs":      runs,
	})
}

func (h *UIHandler) handleResearchLaunch(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	
	query := strings.TrimSpace(r.FormValue("query"))
	if query == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	
	maxQuestions, _ := strconv.Atoi(r.FormValue("max_questions"))
	if maxQuestions <= 0 {
		maxQuestions = 6
	}
	
	orchestrator := research.NewOrchestrator(h.client, h.store, h.searchSvc)
	
	runID, err := h.store.CreateResearchRun(query)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create run: %v", err), http.StatusInternalServerError)
		return
	}
	
	opts := research.Options{
		MaxSubQuestions: maxQuestions,
		ResumeRunID:     runID,
	}
	
	go func() {
		_, _ = orchestrator.Run(context.Background(), query, opts)
	}()
	
	http.Redirect(w, r, fmt.Sprintf("/ui/research/%d", runID), http.StatusSeeOther)
}

func (h *UIHandler) handleResearchDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	runID, _ := strconv.ParseInt(idStr, 10, 64)
	
	run, _ := h.store.GetResearchRun(runID)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	
	h.renderTemplate(w, "base.html", map[string]interface{}{
		"ActiveNav": "research",
		"RunDetail": map[string]interface{}{
			"Run": run,
		},
	})
}

func (h *UIHandler) handleResearchStatus(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	runID, _ := strconv.ParseInt(idStr, 10, 64)
	
	run, _ := h.store.GetResearchRun(runID)
	if run == nil {
		http.NotFound(w, r)
		return
	}
	
	sqs, _ := h.store.GetSubQuestionsForRun(runID)
	findings, _ := h.store.GetAllFindingsForRun(runID)
	
	var reportHTML template.HTML
	if run.ReportMD != "" {
		var buf bytes.Buffer
		if err := h.md.Convert([]byte(run.ReportMD), &buf); err == nil {
			reportHTML = template.HTML(buf.String())
		}
	}
	
	h.renderPartial(w, "_run_status.html", map[string]interface{}{
		"Type":         "research",
		"Status":       run.Status,
		"SubQuestions": sqs,
		"Findings":     findings,
		"ReportHTML":   reportHTML,
	})
}
