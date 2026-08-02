package config

import (
	"strings"
	"testing"
)

func TestValidateNews_ItemsPerField_Defaults(t *testing.T) {
	// Zero ItemsPerField (the "use default" sentinel) must validate
	// clean so operators can rely on ResolveNewsItemsPerField to pick
	// the default. Negative values are rejected — mirrors the
	// existing MaxArticlesPerField rule.
	if err := validateNews(&NewsConfig{ItemsPerField: 0}); err != nil {
		t.Fatalf("validateNews(ItemsPerField=0) returned %v, want nil", err)
	}
}

func TestValidateNews_ItemsPerField_RejectsNegative(t *testing.T) {
	n := &NewsConfig{ItemsPerField: -1}
	if err := validateNews(n); err == nil {
		t.Fatalf("validateNews(ItemsPerField=-1) returned nil, want error")
	}
}

func TestValidateNews_ItemsPerField_RejectsAboveCap(t *testing.T) {
	// Anything above HardNewsItemsPerField must fail with a clear
	// error so a typo in config.yaml doesn't silently fall back.
	n := &NewsConfig{ItemsPerField: HardNewsItemsPerField + 1}
	err := validateNews(n)
	if err == nil {
		t.Fatalf("validateNews(ItemsPerField=%d) returned nil, want error", n.ItemsPerField)
	}
	if !strings.Contains(err.Error(), "exceeds hard cap") {
		t.Fatalf("error %q does not mention 'exceeds hard cap'", err)
	}
}

func TestResolveNewsItemsPerField_Default(t *testing.T) {
	// nil config, nil News block, and zero ItemsPerField all pick
	// the default.
	cases := []*Config{nil, {}, {News: &NewsConfig{}}}
	for _, c := range cases {
		got := c.ResolveNewsItemsPerField()
		if got != DefaultNewsItemsPerField {
			t.Fatalf("ResolveNewsItemsPerField()=%d, want default %d", got, DefaultNewsItemsPerField)
		}
	}
}

func TestResolveNewsItemsPerField_HonorsConfig(t *testing.T) {
	c := &Config{News: &NewsConfig{ItemsPerField: 7}}
	if got := c.ResolveNewsItemsPerField(); got != 7 {
		t.Fatalf("got %d, want 7", got)
	}
}

func TestHardNewsItemsPerField_WithinTelegramBudget(t *testing.T) {
	// Sanity: at the hard cap, 20 items × ~150 chars/short body
	// (2-3 sentence LLM summary) + per-item framing must stay under
	// Telegram's 4096-char single-message limit. If this stops
	// holding, lower the cap.
	const realisticCharsPerItem = 150
	if HardNewsItemsPerField*realisticCharsPerItem >= 4096 {
		t.Fatalf("HardNewsItemsPerField=%d * %d >= 4096 (Telegram limit); lower the cap",
			HardNewsItemsPerField, realisticCharsPerItem)
	}
}
