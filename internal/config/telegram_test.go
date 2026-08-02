package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_TelegramDisabled_OK(t *testing.T) {
	yaml := `opencode_zen:
  base_url: https://example.com
  api_key: sk-test
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
	yaml := `opencode_zen:
  base_url: https://example.com
  api_key: sk-test
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

	yaml := `opencode_zen:
  base_url: https://example.com
  api_key: sk-test
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
	yaml := `opencode_zen:
  base_url: https://example.com
  api_key: sk-test
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
	yaml := `opencode_zen:
  base_url: https://example.com
  api_key: sk-test
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
	yaml := `opencode_zen:
  base_url: https://example.com
  api_key: sk-test
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
