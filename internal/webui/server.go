package webui

import (
	"encoding/json"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
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
	registry  *discovery.Registry
	templates *template.Template
	md        goldmark.Markdown
}

// NewUIHandler creates a new handler for the web UI.
func NewUIHandler(store *store.Store, client *llm.Client, registry *discovery.Registry) (*UIHandler, error) {
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
		store:     store,
		client:    client,
		registry:  registry,
		templates: tmpl,
		md:        md,
	}, nil
}

// RegisterRoutes mounts the UI routes onto a mux
func (h *UIHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ui", h.handleIndex)
	mux.HandleFunc("GET /ui/history", h.handleHistory)
}

func (h *UIHandler) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := h.templates.ExecuteTemplate(w, "index.html", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template rendering error: %v", err), http.StatusInternalServerError)
	}
}

func (h *UIHandler) handleHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	
	history, err := h.store.GetMergedHistory(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	if history == nil {
		history = []store.RunHistoryItem{}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
}
