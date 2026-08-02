package webui

import (
	"context"
	"encoding/json"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
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

// ConfigPath is the on-disk location of config.yaml. The web UI writes back to
// this path when the user updates provider settings. The cmd/onyx/main.go
// entrypoint also reads from "config.yaml" at startup, so a single constant
// keeps both sides in sync.
const ConfigPath = "config.yaml"

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
	mux.HandleFunc("GET /ui/settings", h.handleGetSettings)
	mux.HandleFunc("POST /ui/settings", h.handlePostSettings)
	mux.HandleFunc("GET /ui/models", h.handleListModels)
	mux.HandleFunc("GET /ui/profile", h.handleProfilePage)
}

func (h *UIHandler) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := h.templates.ExecuteTemplate(w, "index.html", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template rendering error: %v", err), http.StatusInternalServerError)
	}
}

func (h *UIHandler) handleProfilePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := h.templates.ExecuteTemplate(w, "profile.html", nil)
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

// settingsResponse is the JSON shape returned to the browser for the
// Settings modal. The API key is always masked here.
type settingsResponse struct {
	Provider       string   `json:"provider"`
	BaseURL        string   `json:"base_url"`
	APIKeyMasked   string   `json:"api_key_masked"`
	APIKeySet       bool          `json:"api_key_set"`
	Model           string        `json:"model"`
	SavedModels     []config.SavedModel `json:"saved_models"`
	Providers       []providerDTO `json:"providers"`
	AvailableModels []string      `json:"available_models"`
	ProviderKeysSet map[string]bool   `json:"provider_keys_set"`
	ProviderURLs    map[string]string `json:"provider_urls"`
}

type providerDTO struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	BaseURL     string   `json:"base_url"`
	Models      []string `json:"models"`
	SupportsList bool    `json:"supports_list"`
}

func (h *UIHandler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	resp := settingsResponse{
		Provider: "opencode_zen",
	}

	// Try the on-disk config first; if it's missing or invalid, fall back to
	// the values currently held by the running client (which were seeded at
	// startup).
	cfg, _ := config.LoadConfig(ConfigPath)
	if cfg != nil && cfg.OpenCodeZen.BaseURL != "" {
		resp.BaseURL = cfg.OpenCodeZen.BaseURL
		resp.APIKeySet = cfg.OpenCodeZen.APIKey != ""
		resp.Model = cfg.OpenCodeZen.DefaultModel
		resp.SavedModels = cfg.OpenCodeZen.SavedModels
		if resp.SavedModels == nil {
			resp.SavedModels = []config.SavedModel{}
		}
		resp.Provider = matchProviderID(cfg.OpenCodeZen.BaseURL)
		
		resp.ProviderKeysSet = make(map[string]bool)
		for k, v := range cfg.OpenCodeZen.ProviderKeys {
			if v != "" {
				resp.ProviderKeysSet[k] = true
			}
		}
		resp.ProviderURLs = cfg.OpenCodeZen.ProviderURLs
		if resp.ProviderURLs == nil {
			resp.ProviderURLs = make(map[string]string)
		}
	} else if h.client != nil {
		baseURL, model, masked := h.client.Snapshot()
		resp.BaseURL = baseURL
		resp.APIKeySet = h.client.RawAPIKey() != ""
		resp.APIKeyMasked = masked
		resp.Model = model
		resp.Provider = matchProviderID(baseURL)
	}

	if cfg != nil {
		resp.APIKeyMasked = maskForDisplay(cfg.OpenCodeZen.APIKey)
	} else if resp.APIKeyMasked == "" && h.client != nil {
		_, _, masked := h.client.Snapshot()
		resp.APIKeyMasked = masked
	}

	// Curated list for the currently configured provider — used as a fallback
	// if the user has not yet entered a base URL + API key.
	models, _ := llm.ListModels(r.Context(), resp.Provider, resp.BaseURL, apiKeyForListing(cfg, h.client))
	resp.AvailableModels = models

	// Static provider list for the dropdown.
	providers := llm.Providers()
	resp.Providers = make([]providerDTO, 0, len(providers))
	for _, p := range providers {
		resp.Providers = append(resp.Providers, providerDTO{
			ID:          p.ID,
			Label:       p.Label,
			BaseURL:     p.BaseURL,
			Models:      p.Models,
			SupportsList: p.SupportsList,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type postSettingsRequest struct {
	Provider    string   `json:"provider"`
	BaseURL     string   `json:"base_url"`
	APIKey      string   `json:"api_key"`
	Model       string              `json:"model"`
	SavedModels []config.SavedModel `json:"saved_models"`
}

func (h *UIHandler) handlePostSettings(w http.ResponseWriter, r *http.Request) {
	var req postSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.BaseURL = strings.TrimSuffix(req.BaseURL, "/")
	if req.BaseURL == "" {
		http.Error(w, "base_url is required", http.StatusBadRequest)
		return
	}
	if req.Model = strings.TrimSpace(req.Model); req.Model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	// Load existing config so we don't clobber unrelated keys.
	cfg, err := config.LoadConfig(ConfigPath)
	if err != nil {
		// Missing file is fine — start a new config.
		cfg = &config.Config{}
	}
	cfg.OpenCodeZen.BaseURL = req.BaseURL
	cfg.OpenCodeZen.DefaultModel = req.Model
	cfg.OpenCodeZen.SavedModels = req.SavedModels
	if cfg.OpenCodeZen.ProviderKeys == nil {
		cfg.OpenCodeZen.ProviderKeys = make(map[string]string)
	}
	if cfg.OpenCodeZen.ProviderURLs == nil {
		cfg.OpenCodeZen.ProviderURLs = make(map[string]string)
	}
	
	if strings.TrimSpace(req.APIKey) != "" {
		cfg.OpenCodeZen.APIKey = strings.TrimSpace(req.APIKey)
		cfg.OpenCodeZen.ProviderKeys[req.Provider] = strings.TrimSpace(req.APIKey)
	}
	cfg.OpenCodeZen.ProviderURLs[req.Provider] = req.BaseURL

	if err := config.SaveConfig(ConfigPath, cfg); err != nil {
		http.Error(w, "failed to persist settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply live to the running client so subsequent chat calls use the new
	// model without requiring a restart.
	if h.client != nil {
		h.client.SetBaseURL(req.BaseURL)
		h.client.SetModel(req.Model)
		if strings.TrimSpace(req.APIKey) != "" {
			h.client.SetAPIKey(strings.TrimSpace(req.APIKey))
		}
	}

	// Best-effort verify the key by listing models with a short timeout.
	verifyCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	models, _ := llm.ListModels(verifyCtx, matchProviderID(req.BaseURL), req.BaseURL, effectiveAPIKey(cfg, req.APIKey))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":               true,
		"provider":         matchProviderID(req.BaseURL),
		"base_url":         req.BaseURL,
		"model":            req.Model,
		"saved_models":     req.SavedModels,
		"api_key_masked":   maskForDisplay(cfg.OpenCodeZen.APIKey),
		"available_models": models,
	})
}

func (h *UIHandler) handleListModels(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	baseURL := strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("base_url")), "/")
	apiKey := strings.TrimSpace(r.URL.Query().Get("api_key"))

	// If the user did not pass an explicit api_key (typical for the model
	// picker when nothing has changed), fall back to the current configured
	// key so the curated list is augmented with the live /models response.
	if apiKey == "" {
		if cfg, err := config.LoadConfig(ConfigPath); err == nil && cfg != nil {
			apiKey = cfg.OpenCodeZen.APIKey
		} else if h.client != nil {
			apiKey = h.client.RawAPIKey()
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	models, err := llm.ListModels(ctx, provider, baseURL, apiKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"provider": provider,
		"base_url": baseURL,
		"models":   models,
	})
}

func matchProviderID(baseURL string) string {
	base := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(baseURL), "/"))
	switch {
	case strings.Contains(base, "opencode.ai/zen"):
		return "opencode_zen"
	case strings.HasPrefix(base, "https://api.openai.com"):
		return "openai"
	case strings.HasPrefix(base, "https://api.anthropic.com"):
		return "anthropic"
	case strings.Contains(base, "api.groq.com"):
		return "groq"
	case strings.Contains(base, "openrouter.ai"):
		return "openrouter"
	}
	return "custom"
}

func maskForDisplay(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		if len(key) <= 2 {
			return "**"
		}
		return key[:2] + strings.Repeat("*", len(key)-2)
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func effectiveAPIKey(cfg *config.Config, override string) string {
	if s := strings.TrimSpace(override); s != "" {
		return s
	}
	if cfg != nil {
		return cfg.OpenCodeZen.APIKey
	}
	return ""
}

func apiKeyForListing(cfg *config.Config, client *llm.Client) string {
	if cfg != nil && cfg.OpenCodeZen.APIKey != "" {
		return cfg.OpenCodeZen.APIKey
	}
	if client != nil {
		return client.RawAPIKey()
	}
	return ""
}
