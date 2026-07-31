package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type OpenCodeZenConfig struct {
	BaseURL      string `yaml:"base_url"`
	APIKey       string `yaml:"api_key"`
	DefaultModel string `yaml:"default_model"`
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

type Config struct {
	OpenCodeZen   OpenCodeZenConfig `yaml:"opencode_zen"`
	ScraperAPIKey string            `yaml:"scraperapi_key"`
	TinyFish      *TinyFishConfig   `yaml:"tinyfish,omitempty"`
	Jina          *JinaConfig       `yaml:"jina,omitempty"`
	Discovery     *DiscoveryConfig  `yaml:"discovery,omitempty"`
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

	return &cfg, nil
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
	}
	return &merged
}

