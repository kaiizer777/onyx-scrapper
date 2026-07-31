package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider describes an LLM provider preset surfaced in the Settings UI.
type Provider struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	BaseURL    string   `json:"base_url"`
	Models     []string `json:"models"`
	SupportsList bool   `json:"supports_list"` // true if provider has a usable GET /v1/models endpoint
}

// Providers returns the catalog of provider presets available in the UI.
// The slice order is the order shown in the dropdown.
func Providers() []Provider {
	return []Provider{
		{
			ID:       "opencode_zen",
			Label:    "OpenCode Zen",
			BaseURL:  "https://opencode.ai/zen/v1",
			SupportsList: false,
			Models: []string{
				"mimo-v2.5-free",
				"gpt-5",
				"gpt-5-mini",
				"gpt-4.1",
				"gpt-4.1-mini",
				"gpt-4o",
				"gpt-4o-mini",
				"claude-sonnet-4",
				"claude-3.5-sonnet",
				"claude-3.5-haiku",
				"gemini-2.5-pro",
				"gemini-2.5-flash",
				"gemini-2.0-flash",
				"deepseek-chat",
				"deepseek-reasoner",
				"llama-3.3-70b",
				"qwen-2.5-72b",
			},
		},
		{
			ID:       "openai",
			Label:    "OpenAI",
			BaseURL:  "https://api.openai.com/v1",
			SupportsList: true,
			Models: []string{
				"gpt-5",
				"gpt-5-mini",
				"gpt-4.1",
				"gpt-4.1-mini",
				"gpt-4.1-nano",
				"gpt-4o",
				"gpt-4o-mini",
				"o3",
				"o3-mini",
				"o4-mini",
			},
		},
		{
			ID:       "anthropic",
			Label:    "Anthropic",
			BaseURL:  "https://api.anthropic.com/v1",
			SupportsList: false,
			Models: []string{
				"claude-opus-4-1",
				"claude-sonnet-4",
				"claude-3-7-sonnet-latest",
				"claude-3-5-sonnet-latest",
				"claude-3-5-haiku-latest",
				"claude-3-opus-latest",
			},
		},
		{
			ID:       "groq",
			Label:    "Groq",
			BaseURL:  "https://api.groq.com/openai/v1",
			SupportsList: true,
			Models: []string{
				"llama-3.3-70b-versatile",
				"llama-3.1-8b-instant",
				"mixtral-8x7b-32768",
				"gemma2-9b-it",
			},
		},
		{
			ID:       "openrouter",
			Label:    "OpenRouter",
			BaseURL:  "https://openrouter.ai/api/v1",
			SupportsList: true,
			Models: []string{
				"openai/gpt-4o",
				"openai/gpt-4o-mini",
				"anthropic/claude-3.5-sonnet",
				"google/gemini-2.0-flash-exp:free",
				"meta-llama/llama-3.3-70b-instruct:free",
				"deepseek/deepseek-chat:free",
			},
		},
		{
			ID:       "custom",
			Label:    "Custom (OpenAI-compatible)",
			BaseURL:  "",
			SupportsList: true,
			Models: []string{},
		},
	}
}

// FindProvider returns the preset matching the given id, or nil if unknown.
func FindProvider(id string) *Provider {
	for _, p := range Providers() {
		if p.ID == id {
			return &p
		}
	}
	return nil
}

// ListModels returns the curated list for a provider, optionally trying the
// provider's /models endpoint first to enrich the list with live model ids.
// If baseURL or apiKey are empty, the curated list is returned as-is.
func ListModels(ctx context.Context, providerID, baseURL, apiKey string) ([]string, error) {
	provider := FindProvider(providerID)
	curated := []string{}
	if provider != nil {
		curated = append(curated, provider.Models...)
	}

	// If we don't have credentials or a base URL, return curated.
	if baseURL == "" || apiKey == "" {
		return dedupe(curated), nil
	}

	// Only attempt live fetch for providers that advertise a /models endpoint.
	// "custom" also qualifies; "anthropic" / "opencode_zen" do not.
	if provider != nil && !provider.SupportsList && provider.ID != "custom" {
		return dedupe(curated), nil
	}

	live, err := fetchRemoteModels(ctx, baseURL, apiKey)
	if err != nil || len(live) == 0 {
		return dedupe(curated), nil
	}
	return dedupe(append(curated, live...)), nil
}

func fetchRemoteModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	baseURL = strings.TrimSuffix(baseURL, "/")
	url := baseURL + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	hc := &http.Client{Timeout: 6 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// OpenAI-compatible: { "data": [ { "id": "..." }, ... ] }
	var openAI struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &openAI); err == nil && len(openAI.Data) > 0 {
		ids := make([]string, 0, len(openAI.Data))
		for _, m := range openAI.Data {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}

	// Fallback shape: { "models": [ { "name": "..." } ] }
	var alt struct {
		Models []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &alt); err == nil && len(alt.Models) > 0 {
		ids := make([]string, 0, len(alt.Models))
		for _, m := range alt.Models {
			if m.ID != "" {
				ids = append(ids, m.ID)
			} else if m.Name != "" {
				ids = append(ids, m.Name)
			}
		}
		return ids, nil
	}

	return nil, fmt.Errorf("unrecognized models response")
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
