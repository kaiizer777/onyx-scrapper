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

// ConfigPath is the on-disk location of config.yaml.
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
	mux.HandleFunc("GET /ui/telegram", h.handleGetTelegramSettings)
	mux.HandleFunc("POST /ui/telegram", h.handlePostTelegramSettings)
	mux.HandleFunc("POST /ui/enhance-prompt", h.handleEnhancePrompt)
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

// providerDTO is the JSON shape for a provider preset in the dropdown.
type providerDTO struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	BaseURL      string   `json:"base_url"`
	Models       []string `json:"models"`
	SupportsList bool     `json:"supports_list"`
}

// providerStateDTO is the JSON shape for a stored provider config sent to the UI.
type providerStateDTO struct {
	APIKeySet    bool   `json:"api_key_set"`
	APIKeyMasked string `json:"api_key_masked"`
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
}

// settingsResponse is the JSON shape returned to the browser for the Settings modal.
type settingsResponse struct {
	// ActiveProvider is the currently selected provider ID.
	ActiveProvider string `json:"active_provider"`

	// ProviderConfigs maps provider ID → stored state (masked key, url, model).
	ProviderConfigs map[string]providerStateDTO `json:"provider_configs"`

	// SavedModels is the cross-provider model bookmark list.
	SavedModels []config.SavedModel `json:"saved_models"`

	// Providers is the static catalog (presets, model lists).
	Providers []providerDTO `json:"providers"`

	// AvailableModels is the live model list for the active provider.
	AvailableModels []string `json:"available_models"`
}

func (h *UIHandler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg, _ := config.LoadConfig(ConfigPath)
	if cfg == nil {
		cfg = &config.Config{}
	}

	// Build the per-provider state map for the UI.
	providerConfigs := make(map[string]providerStateDTO)
	if cfg.Providers != nil {
		for id, p := range cfg.Providers {
			providerConfigs[id] = providerStateDTO{
				APIKeySet:    p.APIKey != "",
				APIKeyMasked: maskForDisplay(p.APIKey),
				BaseURL:      p.BaseURL,
				Model:        p.Model,
			}
		}
	}

	activeProvider := cfg.ActiveProvider
	if activeProvider == "" {
		activeProvider = "opencode_zen"
	}

	// Determine the base URL + API key for the active provider to fetch live models.
	activeP := cfg.ActiveProviderConfig()
	apiKeyForList := activeP.APIKey
	if apiKeyForList == "" && h.client != nil {
		apiKeyForList = h.client.RawAPIKey()
	}
	models, _ := llm.ListModels(r.Context(), activeProvider, activeP.BaseURL, apiKeyForList)

	savedModels := cfg.SavedModels
	if savedModels == nil {
		savedModels = []config.SavedModel{}
	}

	providers := llm.Providers()
	dtos := make([]providerDTO, 0, len(providers))
	for _, p := range providers {
		dtos = append(dtos, providerDTO{
			ID:           p.ID,
			Label:        p.Label,
			BaseURL:      p.BaseURL,
			Models:       p.Models,
			SupportsList: p.SupportsList,
		})
	}

	resp := settingsResponse{
		ActiveProvider:  activeProvider,
		ProviderConfigs: providerConfigs,
		SavedModels:     savedModels,
		Providers:       dtos,
		AvailableModels: models,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// postSettingsRequest is the JSON body for POST /ui/settings.
type postSettingsRequest struct {
	// Provider is the provider ID being saved (e.g. "openai", "anthropic").
	Provider string `json:"provider"`
	// BaseURL is the endpoint for this provider.
	BaseURL string `json:"base_url"`
	// APIKey — blank means "keep the stored key unchanged".
	APIKey string `json:"api_key"`
	// Model is the selected model for this provider (also becomes the active model).
	Model string `json:"model"`
	// SavedModels is the full cross-provider bookmark list.
	SavedModels []config.SavedModel `json:"saved_models"`
}

func (h *UIHandler) handlePostSettings(w http.ResponseWriter, r *http.Request) {
	var req postSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	req.BaseURL = strings.TrimSuffix(strings.TrimSpace(req.BaseURL), "/")
	if req.BaseURL == "" {
		http.Error(w, "base_url is required", http.StatusBadRequest)
		return
	}
	if req.Model = strings.TrimSpace(req.Model); req.Model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	// Load existing config — start fresh if missing.
	cfg, err := config.LoadConfig(ConfigPath)
	if err != nil {
		cfg = &config.Config{}
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}

	// Upsert this provider's entry, preserving the existing key if no new one supplied.
	existing := cfg.Providers[req.Provider]
	if strings.TrimSpace(req.APIKey) != "" {
		existing.APIKey = strings.TrimSpace(req.APIKey)
	}
	existing.BaseURL = req.BaseURL
	existing.Model = req.Model
	cfg.Providers[req.Provider] = existing

	// Update active provider and cross-provider model bookmarks.
	cfg.ActiveProvider = req.Provider
	cfg.SavedModels = req.SavedModels

	if err := config.SaveConfig(ConfigPath, cfg); err != nil {
		http.Error(w, "failed to persist settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply live to the running client immediately.
	if h.client != nil {
		h.client.SetBaseURL(req.BaseURL)
		h.client.SetModel(req.Model)
		if apiKey := cfg.Providers[req.Provider].APIKey; apiKey != "" {
			h.client.SetAPIKey(apiKey)
		}
	}

	// Best-effort verify by listing models.
	verifyCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	models, _ := llm.ListModels(verifyCtx, req.Provider, req.BaseURL, cfg.Providers[req.Provider].APIKey)

	// Build the updated provider_configs map for the response.
	providerConfigs := make(map[string]providerStateDTO)
	for id, p := range cfg.Providers {
		providerConfigs[id] = providerStateDTO{
			APIKeySet:    p.APIKey != "",
			APIKeyMasked: maskForDisplay(p.APIKey),
			BaseURL:      p.BaseURL,
			Model:        p.Model,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":               true,
		"active_provider":  req.Provider,
		"base_url":         req.BaseURL,
		"model":            req.Model,
		"saved_models":     cfg.SavedModels,
		"available_models": models,
		"provider_configs": providerConfigs,
	})
}

func (h *UIHandler) handleListModels(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	baseURL := strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("base_url")), "/")
	apiKey := strings.TrimSpace(r.URL.Query().Get("api_key"))

	// Fall back to the stored key for this provider if none was passed.
	if apiKey == "" {
		if cfg, err := config.LoadConfig(ConfigPath); err == nil && cfg != nil {
			if p, ok := cfg.Providers[provider]; ok {
				apiKey = p.APIKey
			}
		}
		if apiKey == "" && h.client != nil {
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

// telegramSettingsResponse is the JSON shape returned to the browser for the Telegram modal.
type telegramSettingsResponse struct {
	Enabled        bool   `json:"enabled"`
	BotTokenSet    bool   `json:"bot_token_set"`
	BotTokenMasked string `json:"bot_token_masked"`
	AllowedChatIDs string `json:"allowed_chat_ids"` // comma-separated
}

func (h *UIHandler) handleGetTelegramSettings(w http.ResponseWriter, r *http.Request) {
	cfg, _ := config.LoadConfig(ConfigPath)
	if cfg == nil {
		cfg = &config.Config{}
	}

	resp := telegramSettingsResponse{}
	if cfg.Telegram != nil {
		if cfg.Telegram.Enabled != nil {
			resp.Enabled = *cfg.Telegram.Enabled
		}
		resp.BotTokenSet = cfg.Telegram.BotToken != ""
		resp.BotTokenMasked = maskForDisplay(cfg.Telegram.BotToken)

		// Convert []int64 to comma-separated string
		var chatIDs []string
		for _, id := range cfg.Telegram.AllowedChatIDs {
			chatIDs = append(chatIDs, strconv.FormatInt(id, 10))
		}
		resp.AllowedChatIDs = strings.Join(chatIDs, ", ")
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// postTelegramSettingsRequest is the JSON body for POST /ui/telegram.
type postTelegramSettingsRequest struct {
	Enabled        bool   `json:"enabled"`
	BotToken       string `json:"bot_token"` // blank means keep existing
	AllowedChatIDs string `json:"allowed_chat_ids"`
}

func (h *UIHandler) handlePostTelegramSettings(w http.ResponseWriter, r *http.Request) {
	var req postTelegramSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Load existing config
	cfg, err := config.LoadConfig(ConfigPath)
	if err != nil {
		cfg = &config.Config{}
	}
	if cfg.Telegram == nil {
		cfg.Telegram = &config.TelegramConfig{}
	}

	cfg.Telegram.Enabled = &req.Enabled

	if req.BotToken != "" {
		cfg.Telegram.BotToken = strings.TrimSpace(req.BotToken)
	}

	// Parse allowed chat IDs
	parts := strings.Split(req.AllowedChatIDs, ",")
	var ids []int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	cfg.Telegram.AllowedChatIDs = ids

	if err := config.SaveConfig(ConfigPath, cfg); err != nil {
		http.Error(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Return updated state
	h.handleGetTelegramSettings(w, r)
}

type enhancePromptRequest struct {
	Prompt string `json:"prompt"`
}

type enhancePromptResponse struct {
	OriginalPrompt string `json:"original_prompt"`
	EnhancedPrompt string `json:"enhanced_prompt"`
	Enhanced       bool   `json:"enhanced"`
	Error          string `json:"error,omitempty"`
}

func (h *UIHandler) handleEnhancePrompt(w http.ResponseWriter, r *http.Request) {
	var req enhancePromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	rawPrompt := strings.TrimSpace(req.Prompt)
	if rawPrompt == "" {
		http.Error(w, "prompt is empty", http.StatusBadRequest)
		return
	}

	cfg, _ := config.LoadConfig(ConfigPath)
	groqKey := cfg.GetGroqAPIKey()
	if groqKey == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(enhancePromptResponse{
			OriginalPrompt: rawPrompt,
			EnhancedPrompt: rawPrompt,
			Enhanced:       false,
			Error:          "groq API key not set in config.yaml",
		})
		return
	}

	enhanced, err := llm.EnhancePromptWithGroq(r.Context(), groqKey, rawPrompt)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(enhancePromptResponse{
			OriginalPrompt: rawPrompt,
			EnhancedPrompt: rawPrompt,
			Enhanced:       false,
			Error:          err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(enhancePromptResponse{
		OriginalPrompt: rawPrompt,
		EnhancedPrompt: enhanced,
		Enhanced:       true,
	})
}

