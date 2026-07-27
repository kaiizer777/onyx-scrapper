package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaiizer-99/onyx-scrapper/internal/store"
)

func TestLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()

	validYAML := `
jobs:
  - name: "Test Job 1"
    url: "https://example.com"
    interval: "5s"
    render: false
    schema: "article"
  - name: "Test Job 2"
    url: "https://example.org"
    interval: "1m"
    render: true
`
	validPath := filepath.Join(tmpDir, "schedule.yaml")
	if err := os.WriteFile(validPath, []byte(validYAML), 0644); err != nil {
		t.Fatalf("failed to write test schedule file: %v", err)
	}

	cfg, err := LoadConfig(validPath)
	if err != nil {
		t.Fatalf("unexpected LoadConfig error: %v", err)
	}
	if len(cfg.Jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(cfg.Jobs))
	}
	if cfg.Jobs[0].Name != "Test Job 1" || cfg.Jobs[0].Interval != "5s" {
		t.Errorf("job 0 mismatch: %+v", cfg.Jobs[0])
	}
	if !cfg.Jobs[1].Render || cfg.Jobs[1].URL != "https://example.org" {
		t.Errorf("job 1 mismatch: %+v", cfg.Jobs[1])
	}
}

func TestLoadConfig_Invalid(t *testing.T) {
	tmpDir := t.TempDir()

	invalidCases := []struct {
		name string
		yaml string
	}{
		{
			name: "missing url",
			yaml: `jobs: [{name: "bad", interval: "5s"}]`,
		},
		{
			name: "missing interval",
			yaml: `jobs: [{name: "bad", url: "https://example.com"}]`,
		},
		{
			name: "invalid duration format",
			yaml: `jobs: [{name: "bad", url: "https://example.com", interval: "not-a-duration"}]`,
		},
		{
			name: "interval too short",
			yaml: `jobs: [{name: "bad", url: "https://example.com", interval: "500ms"}]`,
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(tmpDir, tc.name+".yaml")
			_ = os.WriteFile(path, []byte(tc.yaml), 0644)
			_, err := LoadConfig(path)
			if err == nil {
				t.Errorf("expected error for case %s, got nil", tc.name)
			}
		})
	}
}

func TestScheduler_RunJob(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintln(w, "<html><body><h1>Scheduled Scrape Test</h1><p>Content for scheduled test page with enough length to satisfy content sufficiency test logic.</p></body></html>")
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	defer st.Close()

	cfg := &Config{
		Jobs: []JobConfig{
			{
				Name:     "Test Server Job",
				URL:      ts.URL,
				Interval: "1s",
				Render:   false,
			},
		},
	}

	sched := NewScheduler(cfg, st, nil)
	ctx := context.Background()

	if err := sched.RunJob(ctx, cfg.Jobs[0]); err != nil {
		t.Fatalf("RunJob failed: %v", err)
	}

	// Verify page was saved into SQLite
	results, err := st.SearchPages("Scheduled Scrape Test")
	if err != nil {
		t.Fatalf("SearchPages failed: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("expected saved page result in SQLite storage, got 0")
	}
}

func TestScheduler_Start_Lifecycle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintln(w, "<html><body><h1>Ticker Lifecycle Test</h1><p>Ticker lifecycle verification payload test text that is sufficiently long and contains enough text content to pass the content sufficiency threshold of 150 characters easily without triggering browser fallback rendering.</p></body></html>")
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_lifecycle.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	defer st.Close()

	cfg := &Config{
		Jobs: []JobConfig{
			{
				Name:     "Ticker Job",
				URL:      ts.URL,
				Interval: "1s",
				Render:   false,
			},
		},
	}

	sched := NewScheduler(cfg, st, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- sched.Start(ctx)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("Scheduler.Start returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Scheduler did not terminate within timeout after context cancellation")
	}
}
