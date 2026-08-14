package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProviderConfig holds credentials and the last-used model for a single provider.
type ProviderConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
}

// SavedModel is a cross-provider bookmark shown in the chat model switcher.
type SavedModel struct {
	Provider string `yaml:"provider" json:"provider"`
	Model    string `yaml:"model" json:"model"`
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

type TeacherConfig struct {
	Enabled                  *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	MaxClarificationRounds   int      `yaml:"max_clarification_rounds,omitempty" json:"max_clarification_rounds,omitempty"`
	MinClarificationRounds   int      `yaml:"min_clarification_rounds,omitempty" json:"min_clarification_rounds,omitempty"`
	SectionWorkerConcurrency int      `yaml:"section_worker_concurrency,omitempty" json:"section_worker_concurrency,omitempty"`
	CritiquePassLimit        int      `yaml:"critique_pass_limit,omitempty" json:"critique_pass_limit,omitempty"`
	DiscoverySources         []string `yaml:"discovery_sources,omitempty" json:"discovery_sources,omitempty"`
	DefaultDepth             string   `yaml:"default_depth,omitempty" json:"default_depth,omitempty"`
}

// TelegramWebhookConfig holds webhook-specific fields. Only used when
// TelegramConfig.Mode == "webhook".
type TelegramWebhookConfig struct {
	PublicURL   string `yaml:"public_url"`
	ListenAddr  string `yaml:"listen_addr"`
	SecretToken string `yaml:"secret_token"`
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
	RateBurst            int                   `yaml:"rate_burst,omitempty"`
	RateRefillPS         float64               `yaml:"rate_refill_per_sec,omitempty"`
}

// legacyOpenCodeZenConfig is used only for migrating old config files.
type legacyOpenCodeZenConfig struct {
	BaseURL      string            `yaml:"base_url"`
	APIKey       string            `yaml:"api_key"`
	DefaultModel string            `yaml:"default_model"`
	SavedModels  []SavedModel      `yaml:"saved_models,omitempty"`
	ProviderKeys map[string]string `yaml:"provider_keys,omitempty"`
	ProviderURLs map[string]string `yaml:"provider_urls,omitempty"`
}

// legacyConfig is used only during migration to read the old schema.
type legacyConfig struct {
	OpenCodeZen legacyOpenCodeZenConfig `yaml:"opencode_zen"`
}

// Config is the application configuration.
type Config struct {
	// ActiveProvider is the provider ID currently in use (e.g. "openai", "anthropic").
	ActiveProvider string `yaml:"active_provider"`

	// Providers holds per-provider credentials and the last-used model.
	// Keyed by the provider ID matching llm.Provider.ID.
	Providers map[string]ProviderConfig `yaml:"providers,omitempty"`

	// SavedModels is the cross-provider bookmark list shown in the chat model switcher.
	SavedModels []SavedModel `yaml:"saved_models,omitempty"`

	ScraperAPIKey string          `yaml:"scraperapi_key"`
	Groq          string          `yaml:"groq,omitempty"`
	TinyFish      *TinyFishConfig `yaml:"tinyfish,omitempty"`
	Jina          *JinaConfig     `yaml:"jina,omitempty"`
	Discovery     *DiscoveryConfig `yaml:"discovery,omitempty"`
	Quality       *QualityConfig  `yaml:"quality,omitempty"`
	Telegram      *TelegramConfig `yaml:"telegram,omitempty"`
	Teacher       *TeacherConfig  `yaml:"teacher,omitempty"`
}

// ActiveProviderConfig returns the ProviderConfig for the currently active
// provider, or a zero value if nothing is configured.
func (c *Config) ActiveProviderConfig() ProviderConfig {
	if c == nil || c.Providers == nil {
		return ProviderConfig{}
	}
	return c.Providers[c.ActiveProvider]
}

// GetGroqAPIKey returns the Groq API key from GROQ_API_KEY env var,
// top-level groq field in config.yaml, or providers["groq"].api_key.
func (c *Config) GetGroqAPIKey() string {
	if envKey := os.Getenv("GROQ_API_KEY"); envKey != "" {
		return envKey
	}
	if c != nil {
		if c.Groq != "" {
			return c.Groq
		}
		if p, ok := c.Providers["groq"]; ok && p.APIKey != "" {
			return p.APIKey
		}
	}
	return ""
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
func (c *Config) IsTelegramEnabled() bool {
	if c == nil || c.Telegram == nil {
		return false
	}
	if c.Telegram.Enabled == nil {
		return false
	}
	return *c.Telegram.Enabled
}

// IsTeacherEnabled reports whether Teacher mode is enabled.
func (c *Config) IsTeacherEnabled() bool {
	if c == nil || c.Teacher == nil {
		return true // enabled by default if not explicitly disabled
	}
	if c.Teacher.Enabled == nil {
		return true
	}
	return *c.Teacher.Enabled
}

// GetTeacherConfig returns the teacher configuration populated with defaults.
func (c *Config) GetTeacherConfig() TeacherConfig {
	tc := TeacherConfig{
		MaxClarificationRounds:   10,
		MinClarificationRounds:   2,
		SectionWorkerConcurrency: 4,
		CritiquePassLimit:        2,
		DiscoverySources:         []string{"searxng", "tinyfish", "jina"},
		DefaultDepth:             "solid working understanding",
	}
	enabled := true
	tc.Enabled = &enabled

	if c == nil || c.Teacher == nil {
		return tc
	}

	if c.Teacher.Enabled != nil {
		tc.Enabled = c.Teacher.Enabled
	}
	if c.Teacher.MaxClarificationRounds > 0 {
		tc.MaxClarificationRounds = c.Teacher.MaxClarificationRounds
	}
	if c.Teacher.MinClarificationRounds > 0 {
		tc.MinClarificationRounds = c.Teacher.MinClarificationRounds
	}
	if c.Teacher.SectionWorkerConcurrency > 0 {
		tc.SectionWorkerConcurrency = c.Teacher.SectionWorkerConcurrency
	}
	if c.Teacher.CritiquePassLimit > 0 {
		tc.CritiquePassLimit = c.Teacher.CritiquePassLimit
	}
	if len(c.Teacher.DiscoverySources) > 0 {
		tc.DiscoverySources = c.Teacher.DiscoverySources
	}
	if c.Teacher.DefaultDepth != "" {
		tc.DefaultDepth = c.Teacher.DefaultDepth
	}

	return tc
}

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

// migrateLegacy detects the old opencode_zen schema in raw YAML bytes and
// converts it into the new providers map. Returns the migrated Config, or nil
// if the file is already in the new format.
func migrateLegacy(data []byte) *Config {
	// Check if the old key exists at all.
	if !strings.Contains(string(data), "opencode_zen:") {
		return nil
	}

	var legacy legacyConfig
	if err := yaml.Unmarshal(data, &legacy); err != nil {
		return nil
	}
	// If opencode_zen block is empty (no base_url), nothing to migrate.
	if legacy.OpenCodeZen.BaseURL == "" && legacy.OpenCodeZen.APIKey == "" {
		return nil
	}

	ocz := legacy.OpenCodeZen
	providers := make(map[string]ProviderConfig)

	// Migrate the primary opencode_zen entry.
	primaryID := "opencode_zen"
	providers[primaryID] = ProviderConfig{
		APIKey:  ocz.APIKey,
		BaseURL: ocz.BaseURL,
		Model:   ocz.DefaultModel,
	}

	// Migrate any additional provider_keys / provider_urls entries.
	for id, key := range ocz.ProviderKeys {
		if id == primaryID {
			// Already handled above but update key if set.
			p := providers[id]
			if key != "" {
				p.APIKey = key
			}
			providers[id] = p
			continue
		}
		p := ProviderConfig{APIKey: key}
		if ocz.ProviderURLs != nil {
			p.BaseURL = ocz.ProviderURLs[id]
		}
		providers[id] = p
	}
	for id, u := range ocz.ProviderURLs {
		if _, exists := providers[id]; !exists {
			providers[id] = ProviderConfig{BaseURL: u}
		}
	}

	cfg := &Config{
		ActiveProvider: primaryID,
		Providers:      providers,
		SavedModels:    ocz.SavedModels,
	}
	return cfg
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	// Transparent migration: if old schema detected, convert in-memory.
	if migrated := migrateLegacy(data); migrated != nil {
		// Still need to parse the rest of the file (telegram, tinyfish, etc.)
		var full Config
		if err := yaml.Unmarshal(data, &full); err != nil {
			return nil, fmt.Errorf("parse config yaml: %w", err)
		}
		migrated.ScraperAPIKey = full.ScraperAPIKey
		migrated.Groq = full.Groq
		migrated.TinyFish = full.TinyFish
		migrated.Jina = full.Jina
		migrated.Discovery = full.Discovery
		migrated.Quality = full.Quality
		migrated.Telegram = full.Telegram
		migrated.Teacher = full.Teacher

		if err := validateTelegram(migrated.Telegram); err != nil {
			return nil, err
		}
		return migrated, nil
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}
	if err := validateTelegram(cfg.Telegram); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveConfig writes the configuration back to disk atomically.
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

	if incoming == nil {
		return &merged
	}

	// Active provider.
	if incoming.ActiveProvider != "" {
		merged.ActiveProvider = incoming.ActiveProvider
	}

	// Providers: deep-merge each entry so keys already stored are never wiped
	// by a partial update (e.g. when switching active model without re-entering a key).
	if incoming.Providers != nil {
		if merged.Providers == nil {
			merged.Providers = make(map[string]ProviderConfig)
		}
		for id, inc := range incoming.Providers {
			ex := merged.Providers[id]
			if inc.APIKey != "" {
				ex.APIKey = inc.APIKey
			}
			if inc.BaseURL != "" {
				ex.BaseURL = inc.BaseURL
			}
			if inc.Model != "" {
				ex.Model = inc.Model
			}
			merged.Providers[id] = ex
		}
	}

	// SavedModels: replace entirely when provided (UI sends the full list).
	if len(incoming.SavedModels) > 0 {
		merged.SavedModels = incoming.SavedModels
	}

	if incoming.ScraperAPIKey != "" {
		merged.ScraperAPIKey = incoming.ScraperAPIKey
	}
	if incoming.Groq != "" {
		merged.Groq = incoming.Groq
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
	if incoming.Teacher != nil {
		merged.Teacher = mergeTeacher(merged.Teacher, incoming.Teacher)
	}

	return &merged
}

func mergeTelegram(existing, incoming *TelegramConfig) *TelegramConfig {
	if existing == nil {
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

func mergeTeacher(existing, incoming *TeacherConfig) *TeacherConfig {
	if existing == nil {
		copy := *incoming
		return &copy
	}
	merged := *existing

	if incoming.Enabled != nil {
		merged.Enabled = incoming.Enabled
	}
	if incoming.MaxClarificationRounds != 0 {
		merged.MaxClarificationRounds = incoming.MaxClarificationRounds
	}
	if incoming.MinClarificationRounds != 0 {
		merged.MinClarificationRounds = incoming.MinClarificationRounds
	}
	if incoming.SectionWorkerConcurrency != 0 {
		merged.SectionWorkerConcurrency = incoming.SectionWorkerConcurrency
	}
	if incoming.CritiquePassLimit != 0 {
		merged.CritiquePassLimit = incoming.CritiquePassLimit
	}
	if len(incoming.DiscoverySources) > 0 {
		merged.DiscoverySources = incoming.DiscoverySources
	}
	if incoming.DefaultDepth != "" {
		merged.DefaultDepth = incoming.DefaultDepth
	}
	return &merged
}

