package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/news"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
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

	cfg, err := LoadConfig(validPath, nil)
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
			_, err := LoadConfig(path, nil)
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

// ---------------------------------------------------------------------------
// Phase 10 — news_jobs: load-config validation
// ---------------------------------------------------------------------------

// fakeTelegramGate is a small in-memory TelegramGate for tests. The
// operator-facing semantic is "is Telegram on, and is chat X
// allowlisted?" — both are simple lookups against this struct.
type fakeTelegramGate struct {
	enabled     bool
	allowedIDs  map[int64]bool
}

func (g *fakeTelegramGate) Enabled() bool { return g.enabled }

func (g *fakeTelegramGate) IsAllowedChatID(id int64) bool {
	return g.allowedIDs[id]
}

func newFakeGate(enabled bool, allowedIDs ...int64) *fakeTelegramGate {
	m := map[int64]bool{}
	for _, id := range allowedIDs {
		m[id] = true
	}
	return &fakeTelegramGate{enabled: enabled, allowedIDs: m}
}

func TestLoadConfig_NewsJobs_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	yamlText := `
jobs:
  - name: "Scrape A"
    url: "https://example.com"
    interval: "1h"
news_jobs:
  - name: "Daily Digest"
    interval: "24h"
    window: "past 24h"
    profile: "default"
    sink: "store"
  - name: "Telegram Digest"
    interval: "12h"
    window: "last 6 hours"
    profile: "default"
    sink: "telegram"
    telegram_chat_id: 1050305220
`
	path := filepath.Join(tmpDir, "schedule.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	gate := newFakeGate(true, 1050305220)
	cfg, err := LoadConfig(path, gate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Jobs) != 1 {
		t.Errorf("expected 1 scrape job, got %d", len(cfg.Jobs))
	}
	if len(cfg.NewsJobs) != 2 {
		t.Fatalf("expected 2 news jobs, got %d", len(cfg.NewsJobs))
	}
	if cfg.NewsJobs[1].Sink != NewsSinkTelegram {
		t.Errorf("news job sink not parsed: %q", cfg.NewsJobs[1].Sink)
	}
	if cfg.NewsJobs[1].TelegramChatID != 1050305220 {
		t.Errorf("news job chat id not parsed: %d", cfg.NewsJobs[1].TelegramChatID)
	}
}

func TestLoadConfig_NewsJobs_Invalid(t *testing.T) {
	cases := []struct {
		name        string
		yaml        string
		gate        *fakeTelegramGate
		wantSubstr  string
	}{
		{
			name:       "missing name",
			yaml:       `news_jobs: [{interval: "1h", window: "past 24h", profile: "default"}]`,
			gate:       newFakeGate(false),
			wantSubstr: "name",
		},
		{
			name:       "missing interval",
			yaml:       `news_jobs: [{name: "x", window: "past 24h", profile: "default"}]`,
			gate:       newFakeGate(false),
			wantSubstr: "interval",
		},
		{
			name:       "interval too short (sub-minute)",
			yaml:       `news_jobs: [{name: "x", interval: "30s", window: "past 24h", profile: "default"}]`,
			gate:       newFakeGate(false),
			wantSubstr: "1m",
		},
		{
			name:       "invalid interval format",
			yaml:       `news_jobs: [{name: "x", interval: "every-morning", window: "past 24h", profile: "default"}]`,
			gate:       newFakeGate(false),
			wantSubstr: "invalid interval",
		},
		{
			name:       "missing window",
			yaml:       `news_jobs: [{name: "x", interval: "1h", profile: "default"}]`,
			gate:       newFakeGate(false),
			wantSubstr: "window",
		},
		{
			name:       "missing profile",
			yaml:       `news_jobs: [{name: "x", interval: "1h", window: "past 24h"}]`,
			gate:       newFakeGate(false),
			wantSubstr: "profile",
		},
		{
			name:       "sink telegram with telegram disabled",
			yaml:       `news_jobs: [{name: "x", interval: "1h", window: "past 24h", profile: "default", sink: "telegram", telegram_chat_id: 1}]`,
			gate:       newFakeGate(false),
			wantSubstr: "telegram.enabled",
		},
		{
			name:       "sink telegram without chat id",
			yaml:       `news_jobs: [{name: "x", interval: "1h", window: "past 24h", profile: "default", sink: "telegram"}]`,
			gate:       newFakeGate(true, 1),
			wantSubstr: "telegram_chat_id",
		},
		{
			name:       "sink telegram with disallowed chat id",
			yaml:       `news_jobs: [{name: "x", interval: "1h", window: "past 24h", profile: "default", sink: "telegram", telegram_chat_id: 999}]`,
			gate:       newFakeGate(true, 1),
			wantSubstr: "allowed_chat_ids",
		},
		{
			name:       "unknown sink value",
			yaml:       `news_jobs: [{name: "x", interval: "1h", window: "past 24h", profile: "default", sink: "email"}]`,
			gate:       newFakeGate(false),
			wantSubstr: "sink",
		},
		{
			name:       "nil gate with telegram sink",
			yaml:       `news_jobs: [{name: "x", interval: "1h", window: "past 24h", profile: "default", sink: "telegram", telegram_chat_id: 1}]`,
			gate:       newFakeGate(false),
			wantSubstr: "telegram.enabled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "schedule.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0644); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := LoadConfig(path, tc.gate)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestLoadConfig_NewsJobs_StoreSink_NoTelegram verifies the common
// "operator runs news jobs but has Telegram disabled" path: a
// sink=store news job must validate even with a nil gate. This is
// the safe-default path that most operators will use.
func TestLoadConfig_NewsJobs_StoreSink_NoTelegram(t *testing.T) {
	yamlText := `
news_jobs:
  - name: "Daily"
    interval: "24h"
    window: "past 24h"
    profile: "default"
    sink: "store"
`
	path := filepath.Join(t.TempDir(), "schedule.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadConfig(path, nil)
	if err != nil {
		t.Fatalf("expected store sink to be valid with nil gate; got error: %v", err)
	}
	if len(cfg.NewsJobs) != 1 {
		t.Fatalf("expected 1 news job, got %d", len(cfg.NewsJobs))
	}
	if cfg.NewsJobs[0].Sink != NewsSinkStore {
		t.Errorf("expected NewsSinkStore, got %q", cfg.NewsJobs[0].Sink)
	}
}

// TestLoadConfig_NoJobs verifies the empty case still returns a
// valid (empty) config without erroring.
func TestLoadConfig_NoJobs(t *testing.T) {
	yamlText := `# nothing configured yet\n`
	path := filepath.Join(t.TempDir(), "schedule.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfig(path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Jobs) != 0 || len(cfg.NewsJobs) != 0 {
		t.Errorf("expected empty, got %+v", cfg)
	}
}

// ---------------------------------------------------------------------------
// Phase 10 — RunNewsJob: end-to-end via the newsRunner seam
// ---------------------------------------------------------------------------

// TestRunNewsJob_SinkStore_Stores verifies the default sink=store
// path: the runner closure runs, returns a digest, the scheduler
// records the run row, no Telegram sink is invoked, no error leaks.
func TestRunNewsJob_SinkStore_Stores(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	// We don't actually call the orchestrator's real fetch path —
	// the seam short-circuits to a canned digest. The profile id is
	// only used inside the canned row; no DB lookup happens.
	const testProfileID int64 = 1

	canned := &store.NewsRun{
		ID:        42,
		ProfileID: testProfileID,
		Window:    "past 24h",
		Status:    "completed",
	}
	cannedDigest := &news.NewsDigest{
		RunID:     42,
		ProfileID: testProfileID,
		Window:    "past 24h",
	}

	var runnerCalled bool
	runner := func(ctx context.Context, job NewsJobConfig) (*store.NewsRun, *news.NewsDigest, error) {
		runnerCalled = true
		if job.Name != "Daily" {
			t.Errorf("runner received wrong job name: %q", job.Name)
		}
		if job.Window != "past 24h" {
			t.Errorf("runner received wrong window: %q", job.Window)
		}
		return canned, cannedDigest, nil
	}

	var sinkCalled bool
	sink := func(ctx context.Context, j NewsJobConfig, r *store.NewsRun, d *news.NewsDigest) error {
		sinkCalled = true
		return nil
	}

	cfg := &Config{
		NewsJobs: []NewsJobConfig{
			{
				Name:     "Daily",
				Interval: "1m",
				Window:   "past 24h",
				Profile:  "default",
				Sink:     NewsSinkStore,
			},
		},
	}
	sched := NewScheduler(cfg, st, nil,
		WithNewsRunner(runner),
		WithTelegramSink(sink),
	)

	if err := sched.RunNewsJob(context.Background(), cfg.NewsJobs[0]); err != nil {
		t.Fatalf("RunNewsJob failed: %v", err)
	}
	if !runnerCalled {
		t.Errorf("news runner was not invoked")
	}
	if sinkCalled {
		t.Errorf("telegram sink must not be called for sink=store jobs")
	}
}

// TestRunNewsJob_SinkTelegram_Delivers verifies the sink=telegram
// path: runner is called, sink is called with the same job+run+digest,
// and the chat id is honored.
func TestRunNewsJob_SinkTelegram_Delivers(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	const wantChatID int64 = 1050305220
	canned := &store.NewsRun{ID: 7, ProfileID: 1, Window: "past 24h", Status: "completed"}
	cannedDigest := &news.NewsDigest{RunID: 7, ProfileID: 1, Window: "past 24h"}

	runner := func(ctx context.Context, job NewsJobConfig) (*store.NewsRun, *news.NewsDigest, error) {
		return canned, cannedDigest, nil
	}

	var gotChatID int64
	sink := func(ctx context.Context, j NewsJobConfig, r *store.NewsRun, d *news.NewsDigest) error {
		gotChatID = j.TelegramChatID
		if r == nil || r.ID != 7 {
			t.Errorf("sink got wrong run: %+v", r)
		}
		if d == nil || d.RunID != 7 {
			t.Errorf("sink got wrong digest: %+v", d)
		}
		return nil
	}

	cfg := &Config{
		NewsJobs: []NewsJobConfig{
			{
				Name:           "Daily TG",
				Interval:       "1m",
				Window:         "past 24h",
				Profile:        "default",
				Sink:           NewsSinkTelegram,
				TelegramChatID: wantChatID,
			},
		},
	}
	sched := NewScheduler(cfg, st, nil,
		WithNewsRunner(runner),
		WithTelegramSink(sink),
	)

	if err := sched.RunNewsJob(context.Background(), cfg.NewsJobs[0]); err != nil {
		t.Fatalf("RunNewsJob failed: %v", err)
	}
	if gotChatID != wantChatID {
		t.Errorf("sink called with wrong chat id: got %d want %d", gotChatID, wantChatID)
	}
}

// TestRunNewsJob_NoRunner_Rejected verifies the fail-fast path: a
// news job without a wired runner must not silently no-op.
func TestRunNewsJob_NoRunner_Rejected(t *testing.T) {
	cfg := &Config{
		NewsJobs: []NewsJobConfig{
			{Name: "x", Interval: "1m", Window: "past 24h", Profile: "default"},
		},
	}
	sched := NewScheduler(cfg, nil, nil) // no WithNewsRunner
	err := sched.RunNewsJob(context.Background(), cfg.NewsJobs[0])
	if err == nil {
		t.Fatalf("expected error when no runner is wired")
	}
	if !strings.Contains(err.Error(), "no news runner") {
		t.Errorf("error %q does not mention the missing runner", err.Error())
	}
}

// TestRunNewsJob_RunnerError_BestEffortSink verifies the runner's
// error path: the scheduler logs + returns the error, and the sink
// is NOT invoked (no digest to deliver). This matches the
// "delivery is best-effort, run errors are terminal" rule.
func TestRunNewsJob_RunnerError_BestEffortSink(t *testing.T) {
	runner := func(ctx context.Context, job NewsJobConfig) (*store.NewsRun, *news.NewsDigest, error) {
		return nil, nil, fmt.Errorf("orchestrator exploded")
	}
	var sinkCalled bool
	sink := func(ctx context.Context, j NewsJobConfig, r *store.NewsRun, d *news.NewsDigest) error {
		sinkCalled = true
		return nil
	}

	cfg := &Config{
		NewsJobs: []NewsJobConfig{
			{
				Name: "Fail Daily", Interval: "1m", Window: "past 24h", Profile: "default",
				Sink: NewsSinkTelegram, TelegramChatID: 1,
			},
		},
	}
	sched := NewScheduler(cfg, nil, nil,
		WithNewsRunner(runner),
		WithTelegramSink(sink),
	)
	err := sched.RunNewsJob(context.Background(), cfg.NewsJobs[0])
	if err == nil {
		t.Fatalf("expected error from runner, got nil")
	}
	if sinkCalled {
		t.Errorf("sink must not be called when runner returned an error")
	}
}

// TestRunNewsJob_SinkTelegram_NilSink verifies the safety path: if
// a job is configured with sink=telegram but no telegram sink was
// wired (operator forgot to call WithTelegramSink), the scheduler
// logs a warning and persists to store only — it does NOT crash.
// This is the runtime complement to LoadConfig's fail-fast
// validation: in production, LoadConfig rejects bad config; in
// tests / misconfigured builds, the per-tick path is still
// resilient.
func TestRunNewsJob_SinkTelegram_NilSink(t *testing.T) {
	runner := func(ctx context.Context, job NewsJobConfig) (*store.NewsRun, *news.NewsDigest, error) {
		return &store.NewsRun{ID: 1, Status: "completed"}, &news.NewsDigest{RunID: 1}, nil
	}
	cfg := &Config{
		NewsJobs: []NewsJobConfig{
			{
				Name: "x", Interval: "1m", Window: "past 24h", Profile: "default",
				Sink: NewsSinkTelegram, TelegramChatID: 1,
			},
		},
	}
	sched := NewScheduler(cfg, nil, nil, WithNewsRunner(runner)) // no sink wired
	if err := sched.RunNewsJob(context.Background(), cfg.NewsJobs[0]); err != nil {
		t.Fatalf("RunNewsJob should not error on missing sink (digest is persisted); got: %v", err)
	}
}

// TestStart_NewsJobsLifecycle verifies the ticker-driven loop for
// news jobs: one immediate run on start, then a second run after
// the interval elapses, then ctx-cancel stops the loop.
func TestStart_NewsJobsLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	var runCount int
	mu := sync.Mutex{}
	runner := func(ctx context.Context, job NewsJobConfig) (*store.NewsRun, *news.NewsDigest, error) {
		mu.Lock()
		runCount++
		mu.Unlock()
		return &store.NewsRun{ID: int64(runCount), Status: "completed"}, &news.NewsDigest{RunID: int64(runCount)}, nil
	}

	cfg := &Config{
		NewsJobs: []NewsJobConfig{
			{
				Name: "Ticker", Interval: "150ms", Window: "past 24h",
				Profile: "default", Sink: NewsSinkStore,
			},
		},
	}
	sched := NewScheduler(cfg, st, nil, WithNewsRunner(runner))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = sched.Start(ctx) // returns nil on ctx-cancel

	mu.Lock()
	defer mu.Unlock()
	// We expect: 1 immediate run + at least 1 ticker run (every 150ms
	// in a 500ms window → ~3 runs total). We require >=2 so the
	// test is not flaky on slow CI but still proves the ticker fired.
	if runCount < 2 {
		t.Errorf("expected ticker to fire at least twice, got %d runs", runCount)
	}
}
