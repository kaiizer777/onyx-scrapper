package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTeacherConfigLoading(t *testing.T) {
	yamlContent := `
active_provider: custom
providers:
  custom:
    api_key: test-key
    base_url: https://example.com/v1
    model: test-model
teacher:
  enabled: true
  max_clarification_rounds: 8
  min_clarification_rounds: 3
  section_worker_concurrency: 6
  critique_pass_limit: 3
  discovery_sources:
    - searxng
    - tinyfish
  default_depth: "deep_dive"
`
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if !cfg.IsTeacherEnabled() {
		t.Fatalf("expected teacher mode to be enabled")
	}

	tc := cfg.GetTeacherConfig()
	if tc.MaxClarificationRounds != 8 {
		t.Fatalf("expected MaxClarificationRounds=8, got %d", tc.MaxClarificationRounds)
	}
	if tc.MinClarificationRounds != 3 {
		t.Fatalf("expected MinClarificationRounds=3, got %d", tc.MinClarificationRounds)
	}
	if tc.SectionWorkerConcurrency != 6 {
		t.Fatalf("expected SectionWorkerConcurrency=6, got %d", tc.SectionWorkerConcurrency)
	}
	if tc.CritiquePassLimit != 3 {
		t.Fatalf("expected CritiquePassLimit=3, got %d", tc.CritiquePassLimit)
	}
	if len(tc.DiscoverySources) != 2 || tc.DiscoverySources[0] != "searxng" || tc.DiscoverySources[1] != "tinyfish" {
		t.Fatalf("unexpected discovery sources: %+v", tc.DiscoverySources)
	}
	if tc.DefaultDepth != "deep_dive" {
		t.Fatalf("expected DefaultDepth='deep_dive', got %q", tc.DefaultDepth)
	}
}

func TestTeacherConfigDefaults(t *testing.T) {
	cfg := &Config{}
	if !cfg.IsTeacherEnabled() {
		t.Fatalf("expected teacher mode enabled by default when not configured")
	}

	tc := cfg.GetTeacherConfig()
	if tc.MaxClarificationRounds != 10 {
		t.Fatalf("expected default MaxClarificationRounds=10, got %d", tc.MaxClarificationRounds)
	}
	if tc.MinClarificationRounds != 2 {
		t.Fatalf("expected default MinClarificationRounds=2, got %d", tc.MinClarificationRounds)
	}
	if tc.SectionWorkerConcurrency != 4 {
		t.Fatalf("expected default SectionWorkerConcurrency=4, got %d", tc.SectionWorkerConcurrency)
	}
	if tc.CritiquePassLimit != 2 {
		t.Fatalf("expected default CritiquePassLimit=2, got %d", tc.CritiquePassLimit)
	}
	if tc.DefaultDepth != "solid working understanding" {
		t.Fatalf("expected default DefaultDepth='solid working understanding', got %q", tc.DefaultDepth)
	}
}
