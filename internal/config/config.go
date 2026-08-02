package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type SavedModel struct {
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model" json:"model"`
}

type OpenCodeZenConfig struct {
	BaseURL      string            `yaml:"base_url"`
	APIKey       string            `yaml:"api_key"`
	DefaultModel string            `yaml:"default_model"`
	SavedModels  []SavedModel      `yaml:"saved_models,omitempty"`
	ProviderKeys map[string]string `yaml:"provider_keys,omitempty"`
	ProviderURLs map[string]string `yaml:"provider_urls,omitempty"`
}

type TinyFishConfig struct {
	Enabled *bool  `yaml:"enabled"`
	APIKey  string `yaml:"api_key"`
}

type JinaConfig struct {
	Enabled         *bool  `yaml:"enabled"`
	APIKey          string `yaml:"api_key"`
	RerankerEnabled *bool  `yaml:"reranker_enabled"`
}

type DiscoveryConfig struct {
	FetchPriority []string `yaml:"fetch_priority"`
}

type QualityEntityFreshnessConfig struct {
	Enabled               *bool    `yaml:"enabled,omitempty"`
	MaxLookupsPerRun      int      `yaml:"max_lookups_per_run,omitempty"`
	SecondSourceProviders []string `yaml:"second_source_providers,omitempty"`
	CacheTTLHours         int      `yaml:"cache_ttl_hours,omitempty"`
}

type QualityFetchIntegrityConfig struct {
	Enabled                 *bool `yaml:"enabled,omitempty"`
	AllowQueryReformulation *bool `yaml:"allow_query_reformulation,omitempty"`
}

type QualitySourceAuthorityConfig struct {
	Enabled         *bool  `yaml:"enabled,omitempty"`
	TiersConfigPath string `yaml:"tiers_config_path,omitempty"`
}

type QualityConfig struct {
	Enabled             *bool                        `yaml:"enabled,omitempty"`
	MaxExtraCallsPerRun int                          `yaml:"max_extra_calls_per_run,omitempty"`
	EntityFreshness     QualityEntityFreshnessConfig `yaml:"entity_freshness,omitempty"`
	FetchIntegrity      QualityFetchIntegrityConfig  `yaml:"fetch_integrity,omitempty"`
	SourceAuthority     QualitySourceAuthorityConfig `yaml:"source_authority,omitempty"`
}

// NewsConfig holds the news-mode configuration. News mode is
// keyless — Google News RSS is the primary headline source and
// requires no signup. Operators do NOT need to configure any news
// API key (NewsAPI / GNews / NewsData / Mediastack) to run /news.
type NewsConfig struct {
	DefaultWindow       string `yaml:"default_window,omitempty"`
	ArticlesPerField    int    `yaml:"articles_per_field,omitempty"`
	MinArticlesBackfill int    `yaml:"min_articles_backfill,omitempty"`
	// MaxFields caps the number of profile fields a single news run
	// is allowed to fetch. Bounded to prevent a misconfigured profile
	// (e.g. 100 fields) from silently burning a giant RSS / LLM
	// budget in one /news call. Default: 10.
	MaxFields int `yaml:"max_fields,omitempty"`
	// MaxArticlesPerField caps the per-field item count even if the
	// orchestrator's default ArticlesPerField is raised. This is a
	// second-line guardrail: the cost ceiling is dominated by
	// quality.Budget (MaxExtraCallsPerRun) for full-text pulls, but
	// this cap stops an RSS-flood from a single field from going
	// unbounded. Default: 20.
	MaxArticlesPerField int `yaml:"max_articles_per_field,omitempty"`
	// ItemsPerField caps the number of items rendered per field in
	// the final digest across all surfaces (Web UI / CLI / Telegram /
	// saved Markdown). It is a DISPLAY-TIME cap, applied after the
	// orchestrator's fetch-time budget has already run, so a long
	// fetch can still produce a tight, readable card. Separate from
	// MaxArticlesPerField (which bounds the fetch) on purpose:
	// operators may want to fetch 20 to give the LLM a richer
	// ranking pool but only display the top 10. Default: 10.
	// Hard cap: 20.
	ItemsPerField int `yaml:"items_per_field,omitempty"`
}

// Default and hard cap constants for NewsConfig validation. These
// are exported so the orchestrator can apply the same caps
// regardless of whether config was loaded.
const (
	DefaultNewsMaxFields         = 10
	DefaultNewsMaxArticlesPerField = 20
	HardNewsMaxFields             = 50
	HardNewsMaxArticlesPerField   = 50
	// DefaultNewsItemsPerField is the default number of items
	// rendered per field in the final digest card. Operators can
	// raise it up to HardNewsItemsPerField.
	DefaultNewsItemsPerField = 10
	// HardNewsItemsPerField is the absolute ceiling for
	// ItemsPerField. A misconfigured 1000 here would blow past the
	// Telegram 4096-char message limit on a single field, so we
	// stop well under that.
	HardNewsItemsPerField = 20
)

// TelegramWebhookConfig holds webhook-specific fields. Only used when
// TelegramConfig.Mode == "webhook".
type TelegramWebhookConfig struct {
	PublicURL    string `yaml:"public_url"`
	ListenAddr   string `yaml:"listen_addr"`
	SecretToken  string `yaml:"secret_token"`
}

// TelegramConfig is the chat-gateway config block. When Enabled is true,
// the bot_token (or TELEGRAM_BOT_TOKEN env var) must be non-empty.
type TelegramConfig struct {
	Enabled              *bool                 `yaml:"enabled,omitempty"`
	BotToken             string                `yaml:"bot_token"`
	Mode                 string                `yaml:"mode"`
	Webhook              TelegramWebhookConfig `yaml:"webhook"`
	AllowedChatIDs       []int64               `yaml:"allowed_chat_ids"`
	AllowedUsernames     []string              `yaml:"allowed_usernames"`
	DefaultMode          string                `yaml:"default_mode"`
	MaxConcurrentSessions int                  `yaml:"max_concurrent_sessions"`
	TypingIndicator      *bool                 `yaml:"typing_indicator,omitempty"`
	// Rate limit (Phase 9). Burst is the max number of commands a
	// single chat can fire before the token bucket has to refill.
	// RefillPerSec is the steady-state throughput. Zero / missing
	// falls back to DefaultRateBurst / DefaultRateRefillPerSec in
	// the gateway.
	RateBurst     int     `yaml:"rate_burst,omitempty"`
	RateRefillPS  float64 `yaml:"rate_refill_per_sec,omitempty"`
}

type Config struct {
	OpenCodeZen   OpenCodeZenConfig `yaml:"opencode_zen"`
	ScraperAPIKey string            `yaml:"scraperapi_key"`
	TinyFish      *TinyFishConfig   `yaml:"tinyfish,omitempty"`
	Jina          *JinaConfig       `yaml:"jina,omitempty"`
	Discovery     *DiscoveryConfig  `yaml:"discovery,omitempty"`
	Quality       *QualityConfig    `yaml:"quality,omitempty"`
	Telegram      *TelegramConfig   `yaml:"telegram,omitempty"`
	News          *NewsConfig       `yaml:"news,omitempty"`
}

// GetScraperAPIKey returns the ScraperAPI key from env var SCRAPERAPI_KEY or config file.
func (c *Config) GetScraperAPIKey() string {
	if envKey := os.Getenv("SCRAPERAPI_KEY"); envKey != "" {
		return envKey
	}
	if c != nil {
		return c.ScraperAPIKey
	}
	return ""
}

// GetTelegramBotToken returns the Telegram bot token from the TELEGRAM_BOT_TOKEN
// env var (preferred — keeps secrets out of config.yaml) or the yaml field.
// Returns "" if neither is set.
func (c *Config) GetTelegramBotToken() string {
	if envTok := os.Getenv("TELEGRAM_BOT_TOKEN"); envTok != "" {
		return envTok
	}
	if c != nil && c.Telegram != nil {
		return c.Telegram.BotToken
	}
	return ""
}

// IsTelegramEnabled reports whether the Telegram gateway should start.
// Honors *bool nil-safe semantics used elsewhere in the config: nil/true => enabled.
func (c *Config) IsTelegramEnabled() bool {
	if c == nil || c.Telegram == nil {
		return false
	}
	if c.Telegram.Enabled == nil {
		return false
	}
	return *c.Telegram.Enabled
}

// validateTelegram enforces the invariants the rest of the gateway code relies
// on: non-empty token when enabled, sane mode, and webhook requires HTTPS public_url.
func validateTelegram(t *TelegramConfig) error {
	if t == nil {
		return nil
	}

	if t.Enabled != nil && *t.Enabled {
		if strings.TrimSpace(t.BotToken) == "" && strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")) == "" {
			return fmt.Errorf("invalid telegram config: bot_token is required (or set TELEGRAM_BOT_TOKEN env var) when enabled: true")
		}
		if t.BotToken != "" && strings.TrimSpace(t.BotToken) == "YOUR_BOT_TOKEN" {
			return fmt.Errorf("invalid telegram config: bot_token is still the placeholder 'YOUR_BOT_TOKEN' — set a real token or TELEGRAM_BOT_TOKEN env var")
		}
	}

	mode := strings.ToLower(strings.TrimSpace(t.Mode))
	switch mode {
	case "", "polling", "webhook":
		// ok
	default:
		return fmt.Errorf("invalid telegram config: mode %q must be 'polling' or 'webhook'", t.Mode)
	}

	if mode == "webhook" {
		pub := strings.TrimSpace(t.Webhook.PublicURL)
		if pub == "" {
			return fmt.Errorf("invalid telegram config: webhook.public_url is required when mode=webhook")
		}
		u, err := url.Parse(pub)
		if err != nil {
			return fmt.Errorf("invalid telegram config: webhook.public_url is not a valid URL: %w", err)
		}
		if u.Scheme != "https" {
			return fmt.Errorf("invalid telegram config: webhook.public_url must use https:// scheme (got %q)", u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("invalid telegram config: webhook.public_url is missing a host")
		}
	}

	if t.MaxConcurrentSessions < 0 {
		return fmt.Errorf("invalid telegram config: max_concurrent_sessions must be >= 0")
	}

	if t.RateBurst < 0 {
		return fmt.Errorf("invalid telegram config: rate_burst must be >= 0 (0 = use default 6)")
	}
	if t.RateRefillPS < 0 {
		return fmt.Errorf("invalid telegram config: rate_refill_per_sec must be >= 0 (0 = use default 2.0)")
	}

	if dm := strings.ToLower(strings.TrimSpace(t.DefaultMode)); dm != "" {
		switch dm {
		case "agent", "deep-research":
			// ok
		default:
			return fmt.Errorf("invalid telegram config: default_mode %q must be 'agent' or 'deep-research'", t.DefaultMode)
		}
	}

	return nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}

	if cfg.OpenCodeZen.BaseURL == "" || cfg.OpenCodeZen.APIKey == "" {
		return nil, fmt.Errorf("invalid opencode_zen config: base_url and api_key are required")
	}

	if err := validateTelegram(cfg.Telegram); err != nil {
		return nil, err
	}

	if err := validateNews(cfg.News); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// validateNews enforces Phase 11 cost-guardrail invariants for the
// news-mode block. Bounds are deliberately conservative: a single
// /news run must not silently burn the LLM / search budget.
//
//   - ArticlesPerField and MinArticlesBackfill must be non-negative.
//   - MaxFields and MaxArticlesPerField (if set) must be > 0 and not
//     exceed the hard cap. Zero/negative falls back to the default
//     so operators can opt in by setting a value, not by leaving it
//     blank.
func validateNews(n *NewsConfig) error {
	if n == nil {
		return nil
	}
	if n.ArticlesPerField < 0 {
		return fmt.Errorf("invalid news config: articles_per_field must be >= 0")
	}
	if n.MinArticlesBackfill < 0 {
		return fmt.Errorf("invalid news config: min_articles_backfill must be >= 0")
	}
	if n.MaxFields < 0 {
		return fmt.Errorf("invalid news config: max_fields must be >= 0 (0 = use default %d)", DefaultNewsMaxFields)
	}
	if n.MaxFields > HardNewsMaxFields {
		return fmt.Errorf("invalid news config: max_fields=%d exceeds hard cap %d", n.MaxFields, HardNewsMaxFields)
	}
	if n.MaxArticlesPerField < 0 {
		return fmt.Errorf("invalid news config: max_articles_per_field must be >= 0 (0 = use default %d)", DefaultNewsMaxArticlesPerField)
	}
	if n.MaxArticlesPerField > HardNewsMaxArticlesPerField {
		return fmt.Errorf("invalid news config: max_articles_per_field=%d exceeds hard cap %d", n.MaxArticlesPerField, HardNewsMaxArticlesPerField)
	}
	if n.ItemsPerField < 0 {
		return fmt.Errorf("invalid news config: items_per_field must be >= 0 (0 = use default %d)", DefaultNewsItemsPerField)
	}
	if n.ItemsPerField > HardNewsItemsPerField {
		return fmt.Errorf("invalid news config: items_per_field=%d exceeds hard cap %d", n.ItemsPerField, HardNewsItemsPerField)
	}
	return nil
}

// ResolveNewsCaps returns the effective MaxFields and MaxArticlesPerField
// for a news run, falling back to the defaults when the config value
// is zero. The orchestrator should call this instead of reading
// cfg.News.MaxFields / MaxArticlesPerField directly.
func (c *Config) ResolveNewsCaps() (maxFields, maxArticlesPerField int) {
	maxFields = DefaultNewsMaxFields
	maxArticlesPerField = DefaultNewsMaxArticlesPerField
	if c != nil && c.News != nil {
		if c.News.MaxFields > 0 {
			maxFields = c.News.MaxFields
		}
		if c.News.MaxArticlesPerField > 0 {
			maxArticlesPerField = c.News.MaxArticlesPerField
		}
	}
	return maxFields, maxArticlesPerField
}

// ResolveNewsItemsPerField returns the effective ItemsPerField for
// the news digest renderer, falling back to the default when the
// config value is zero. The view-build layer calls this so the
// display-time cap is consistent across live runs and post-run
// re-renders from the store.
func (c *Config) ResolveNewsItemsPerField() int {
	if c != nil && c.News != nil && c.News.ItemsPerField > 0 {
		return c.News.ItemsPerField
	}
	return DefaultNewsItemsPerField
}

// SaveConfig writes the configuration back to disk in stable key order so
// the resulting file is friendly to diffs and to the eye. Existing keys that
// are zero-valued in the supplied Config are preserved from disk.
func SaveConfig(path string, cfg *Config) error {
	merged := cfg
	if existing, err := LoadConfig(path); err == nil && existing != nil {
		merged = mergeConfig(existing, cfg)
	}
	if merged == nil {
		return fmt.Errorf("no config to save")
	}

	data, err := yaml.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal config yaml: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

func mergeConfig(existing, incoming *Config) *Config {
	merged := *existing

	if incoming != nil {
		if incoming.OpenCodeZen.BaseURL != "" {
			merged.OpenCodeZen.BaseURL = incoming.OpenCodeZen.BaseURL
		}
		if incoming.OpenCodeZen.APIKey != "" {
			merged.OpenCodeZen.APIKey = incoming.OpenCodeZen.APIKey
		}
		if incoming.OpenCodeZen.DefaultModel != "" {
			merged.OpenCodeZen.DefaultModel = incoming.OpenCodeZen.DefaultModel
		}
		if len(incoming.OpenCodeZen.SavedModels) > 0 {
			merged.OpenCodeZen.SavedModels = incoming.OpenCodeZen.SavedModels
		}
		if incoming.ScraperAPIKey != "" {
			merged.ScraperAPIKey = incoming.ScraperAPIKey
		}
		if incoming.TinyFish != nil {
			merged.TinyFish = incoming.TinyFish
		}
		if incoming.Jina != nil {
			merged.Jina = incoming.Jina
		}
		if incoming.Discovery != nil {
			merged.Discovery = incoming.Discovery
		}
		if incoming.Quality != nil {
			if merged.Quality == nil {
				merged.Quality = incoming.Quality
			} else {
				if incoming.Quality.Enabled != nil {
					merged.Quality.Enabled = incoming.Quality.Enabled
				}
				if incoming.Quality.MaxExtraCallsPerRun != 0 {
					merged.Quality.MaxExtraCallsPerRun = incoming.Quality.MaxExtraCallsPerRun
				}
				if incoming.Quality.EntityFreshness.Enabled != nil {
					merged.Quality.EntityFreshness.Enabled = incoming.Quality.EntityFreshness.Enabled
				}
				if incoming.Quality.EntityFreshness.MaxLookupsPerRun != 0 {
					merged.Quality.EntityFreshness.MaxLookupsPerRun = incoming.Quality.EntityFreshness.MaxLookupsPerRun
				}
				if len(incoming.Quality.EntityFreshness.SecondSourceProviders) > 0 {
					merged.Quality.EntityFreshness.SecondSourceProviders = incoming.Quality.EntityFreshness.SecondSourceProviders
				}
				if incoming.Quality.EntityFreshness.CacheTTLHours != 0 {
					merged.Quality.EntityFreshness.CacheTTLHours = incoming.Quality.EntityFreshness.CacheTTLHours
				}
				if incoming.Quality.FetchIntegrity.Enabled != nil {
					merged.Quality.FetchIntegrity.Enabled = incoming.Quality.FetchIntegrity.Enabled
				}
				if incoming.Quality.FetchIntegrity.AllowQueryReformulation != nil {
					merged.Quality.FetchIntegrity.AllowQueryReformulation = incoming.Quality.FetchIntegrity.AllowQueryReformulation
				}
				if incoming.Quality.SourceAuthority.Enabled != nil {
					merged.Quality.SourceAuthority.Enabled = incoming.Quality.SourceAuthority.Enabled
				}
				if incoming.Quality.SourceAuthority.TiersConfigPath != "" {
					merged.Quality.SourceAuthority.TiersConfigPath = incoming.Quality.SourceAuthority.TiersConfigPath
				}
			}
		}
		if incoming.Telegram != nil {
			merged.Telegram = mergeTelegram(merged.Telegram, incoming.Telegram)
		}
		if incoming.News != nil {
			merged.News = incoming.News
		}
	}
	return &merged
}

func mergeTelegram(existing, incoming *TelegramConfig) *TelegramConfig {
	if existing == nil {
		// Copy incoming so caller mutations don't leak.
		copy := *incoming
		return &copy
	}
	merged := *existing

	if incoming.Enabled != nil {
		merged.Enabled = incoming.Enabled
	}
	if incoming.BotToken != "" {
		merged.BotToken = incoming.BotToken
	}
	if incoming.Mode != "" {
		merged.Mode = incoming.Mode
	}
	if incoming.Webhook.PublicURL != "" {
		merged.Webhook.PublicURL = incoming.Webhook.PublicURL
	}
	if incoming.Webhook.ListenAddr != "" {
		merged.Webhook.ListenAddr = incoming.Webhook.ListenAddr
	}
	if incoming.Webhook.SecretToken != "" {
		merged.Webhook.SecretToken = incoming.Webhook.SecretToken
	}
	if len(incoming.AllowedChatIDs) > 0 {
		merged.AllowedChatIDs = incoming.AllowedChatIDs
	}
	if len(incoming.AllowedUsernames) > 0 {
		merged.AllowedUsernames = incoming.AllowedUsernames
	}
	if incoming.DefaultMode != "" {
		merged.DefaultMode = incoming.DefaultMode
	}
	if incoming.MaxConcurrentSessions != 0 {
		merged.MaxConcurrentSessions = incoming.MaxConcurrentSessions
	}
	if incoming.TypingIndicator != nil {
		merged.TypingIndicator = incoming.TypingIndicator
	}
	if incoming.RateBurst != 0 {
		merged.RateBurst = incoming.RateBurst
	}
	if incoming.RateRefillPS != 0 {
		merged.RateRefillPS = incoming.RateRefillPS
	}
	return &merged
}

