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

type Config struct {
	OpenCodeZen   OpenCodeZenConfig `yaml:"opencode_zen"`
	ScraperAPIKey string            `yaml:"scraperapi_key"`
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

