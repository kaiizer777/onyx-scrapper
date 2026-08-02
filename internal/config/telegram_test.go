package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_TelegramDisabled_OK(t *testing.T) {
	yaml := `active_provider: openai
providers:
  openai:
    api_key: sk-test
    base_url: https://api.openai.com/v1
    model: gpt-4o
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Telegram != nil {
		t.Fatalf("expected Telegram to be nil when block omitted, got %+v", cfg.Telegram)
	}
	if cfg.IsTelegramEnabled() {
		t.Fatal("expected IsTelegramEnabled()=false when block omitted")
	}
}

func TestLoadConfig_TelegramEnabled_MissingToken_Errors(t *testing.T) {
	yaml := `active_provider: openai
providers:
  openai:
    api_key: sk-test
    base_url: https://api.openai.com/v1
    model: gpt-4o
telegram:
  enabled: true
  mode: polling
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for enabled telegram with no token")
	}
	if !strings.Contains(err.Error(), "bot_token") {
		t.Fatalf("expected error to mention bot_token, got: %v", err)
	}
}

func TestLoadConfig_TelegramEnabled_EnvToken_Ok(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:env-token")

	yaml := `active_provider: openai
providers:
  openai:
    api_key: sk-test
    base_url: https://api.openai.com/v1
    model: gpt-4o
telegram:
  enabled: true
  mode: polling
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.IsTelegramEnabled() {
		t.Fatal("expected IsTelegramEnabled()=true")
	}
	if got := cfg.GetTelegramBotToken(); got != "123:env-token" {
		t.Fatalf("expected env token, got %q", got)
	}
}

func TestLoadConfig_TelegramEnabled_PlaceholderToken_Errors(t *testing.T) {
	yaml := `active_provider: openai
providers:
  openai:
    api_key: sk-test
    base_url: https://api.openai.com/v1
    model: gpt-4o
telegram:
  enabled: true
  bot_token: "YOUR_BOT_TOKEN"
  mode: polling
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("expected placeholder-token error, got: %v", err)
	}
}

func TestLoadConfig_TelegramWebhook_RequiresHTTPS(t *testing.T) {
	yaml := `active_provider: openai
providers:
  openai:
    api_key: sk-test
    base_url: https://api.openai.com/v1
    model: gpt-4o
telegram:
  enabled: true
  bot_token: "123:real"
  mode: webhook
  webhook:
    public_url: "http://example.com/hook"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected https requirement error, got: %v", err)
	}
}

func TestLoadConfig_TelegramWebhook_ValidHTTPS_Ok(t *testing.T) {
	yaml := `active_provider: openai
providers:
  openai:
    api_key: sk-test
    base_url: https://api.openai.com/v1
    model: gpt-4o
telegram:
  enabled: true
  bot_token: "123:real"
  mode: webhook
  webhook:
    public_url: "https://bot.example.com/telegram/webhook/secret"
    secret_token: "secret"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Telegram.Webhook.PublicURL != "https://bot.example.com/telegram/webhook/secret" {
		t.Fatalf("public_url not preserved: %+v", cfg.Telegram.Webhook)
	}
}

func TestLoadConfig_LegacyMigration(t *testing.T) {
	// Ensure the old opencode_zen schema is transparently migrated.
	yaml := `opencode_zen:
  base_url: https://api.example.com/v1
  api_key: sk-legacy
  default_model: gpt-4o
  provider_keys:
    anthropic: sk-ant-legacy
  provider_urls:
    anthropic: https://api.anthropic.com/v1
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error during migration: %v", err)
	}
	if cfg.ActiveProvider != "opencode_zen" {
		t.Fatalf("expected active_provider=opencode_zen, got %q", cfg.ActiveProvider)
	}
	p := cfg.Providers["opencode_zen"]
	if p.APIKey != "sk-legacy" {
		t.Fatalf("expected opencode_zen api_key=sk-legacy, got %q", p.APIKey)
	}
	if p.Model != "gpt-4o" {
		t.Fatalf("expected opencode_zen model=gpt-4o, got %q", p.Model)
	}
	ant := cfg.Providers["anthropic"]
	if ant.APIKey != "sk-ant-legacy" {
		t.Fatalf("expected anthropic api_key=sk-ant-legacy, got %q", ant.APIKey)
	}
	if ant.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("expected anthropic base_url, got %q", ant.BaseURL)
	}
}
