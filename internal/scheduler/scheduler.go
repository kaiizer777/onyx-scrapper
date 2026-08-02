package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/kaiizer777/onyx-scrapper/internal/extract"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/news"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Scrape job config (Phase 1 — existing).
// ---------------------------------------------------------------------------

// JobConfig defines a scheduled scraping job.
type JobConfig struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Interval string `yaml:"interval"`
	Render   bool   `yaml:"render"`
	Schema   string `yaml:"schema"`
}

// ---------------------------------------------------------------------------
// News job config (Phase 10 — new).
// ---------------------------------------------------------------------------

// NewsJobSink is the delivery target for a recurring news job. Kept as a
// string (not a typed enum) so the YAML surface stays human-readable.
type NewsJobSink string

const (
	// NewsSinkStore persists the digest to news_runs + news_items only.
	// The operator pulls it later via GET /news/{id} or /history. This is
	// the safe default — works even when Telegram is disabled.
	NewsSinkStore NewsJobSink = "store"
	// NewsSinkTelegram delivers the per-field digest to a single chat
	// after the run completes. Requires telegram.enabled=true and the
	// chat id must be in telegram.allowed_chat_ids — both checked at
	// LoadConfig time (fail-fast, not run-time).
	NewsSinkTelegram NewsJobSink = "telegram"
)

// NewsJobConfig defines a scheduled news digest job (Phase 10).
//
// `interval` is a Go duration string ("1h", "24h", "30m"). The minimum
// allowed interval is 1 minute so a misconfigured operator doesn't
// hammer Google News RSS.
//
// `window` is the fixed recency phrase passed to the Phase 3 parser
// ("past 24h", "last 7 days", "today"). Cannot be empty.
//
// `profile` is the profile label resolved by Phase 1's
// GetOrCreateDefaultProfile. "default" is the canonical name and is the
// most common choice for a single-operator setup.
//
// `field` is an optional single-field debug filter — same semantics as
// the CLI's `onyx news --field`. Empty means "all enabled fields".
//
// `sink` and `telegram_chat_id` are validated at LoadConfig time so a
// bad config never starts a daemon that silently drops digests.
type NewsJobConfig struct {
	Name           string       `yaml:"name"`
	Interval       string       `yaml:"interval"`
	Window         string       `yaml:"window"`
	Profile        string       `yaml:"profile"`
	Field          string       `yaml:"field"`
	Sink           NewsJobSink  `yaml:"sink"`
	TelegramChatID int64        `yaml:"telegram_chat_id"`
}

// ---------------------------------------------------------------------------
// Top-level config.
// ---------------------------------------------------------------------------

// Config holds all scheduled job configurations. Sibling keys
// (Jobs + NewsJobs) are dispatched independently by the same Start loop
// — a misconfigured news_jobs entry cannot stop scrape jobs from
// running, and vice versa.
type Config struct {
	Jobs     []JobConfig     `yaml:"jobs"`
	NewsJobs []NewsJobConfig `yaml:"news_jobs"`
}

// ---------------------------------------------------------------------------
// Validation dependencies.
//
// LoadConfig needs to know whether Telegram is enabled and which chat
// ids are allowlisted, so it can fail-fast on bad news_jobs. The
// scheduler package stays decoupled from internal/config — we accept
// the answers as small interfaces that the caller fills in.
//
// This is the same decoupling pattern the rest of the codebase uses
// (Telegram runners, agent registry) — keep internal/scheduler free of
// heavy imports so it stays easy to test.
// ---------------------------------------------------------------------------

// TelegramGate is the minimal contract LoadConfig needs to validate
// news_jobs entries with sink=telegram. internal/config.Config and
// internal/telegram.BotConfig both satisfy it in practice.
type TelegramGate interface {
	// Enabled returns true when Telegram is configured and ready.
	Enabled() bool
	// IsAllowedChatID returns true if chatID is on the operator's
	// allowlist (matching internal/telegram.Authenticator semantics).
	IsAllowedChatID(chatID int64) bool
}

// LoadConfig reads and parses a schedule configuration file. If tg is
// non-nil, news_jobs entries with sink=telegram are validated against
// the Telegram gate. Pass nil to skip that validation (existing tests
// and the no-Telegram case).
func LoadConfig(filePath string, tg TelegramGate) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read schedule config file %s: %w", filePath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse schedule config YAML: %w", err)
	}

	// Existing scrape job validation — untouched.
	for i, job := range cfg.Jobs {
		if job.URL == "" {
			return nil, fmt.Errorf("job #%d (%s) missing required 'url'", i+1, job.Name)
		}
		if job.Interval == "" {
			return nil, fmt.Errorf("job #%d (%s) missing required 'interval'", i+1, job.Name)
		}
		dur, err := time.ParseDuration(job.Interval)
		if err != nil {
			return nil, fmt.Errorf("job #%d (%s) has invalid interval %q: %w", i+1, job.Name, job.Interval, err)
		}
		if dur < 1*time.Second {
			return nil, fmt.Errorf("job #%d (%s) interval %q must be at least 1s", i+1, job.Name, job.Interval)
		}
	}

	// Phase 10 news job validation.
	for i, nj := range cfg.NewsJobs {
		if err := validateNewsJob(i+1, nj, tg); err != nil {
			return nil, err
		}
	}

	return &cfg, nil
}

// validateNewsJob enforces the invariants Phase 10's fail-fast design
// requires. Called by LoadConfig before the daemon starts.
func validateNewsJob(idx int, nj NewsJobConfig, tg TelegramGate) error {
	name := strings.TrimSpace(nj.Name)
	if name == "" {
		return fmt.Errorf("news_jobs #%d missing required 'name'", idx)
	}

	interval := strings.TrimSpace(nj.Interval)
	if interval == "" {
		return fmt.Errorf("news_jobs #%d (%s) missing required 'interval'", idx, name)
	}
	dur, err := time.ParseDuration(interval)
	if err != nil {
		return fmt.Errorf("news_jobs #%d (%s) has invalid interval %q: %w", idx, name, interval, err)
	}
	// 1-minute floor — a typo of "1s" would otherwise hammer RSS and
	// burn through the orchestrator's quality budget in seconds.
	if dur < 1*time.Minute {
		return fmt.Errorf("news_jobs #%d (%s) interval %q must be at least 1m (recurring news is meant for digests, not real-time polling)", idx, name, interval)
	}

	window := strings.TrimSpace(nj.Window)
	if window == "" {
		return fmt.Errorf("news_jobs #%d (%s) missing required 'window' (e.g. 'past 24h', 'last 7 days')", idx, name)
	}

	if strings.TrimSpace(nj.Profile) == "" {
		return fmt.Errorf("news_jobs #%d (%s) missing required 'profile' (use 'default' for the operator's default profile)", idx, name)
	}

	sink := NewsJobSink(strings.ToLower(strings.TrimSpace(string(nj.Sink))))
	if sink == "" {
		sink = NewsSinkStore
	}
	switch sink {
	case NewsSinkStore, NewsSinkTelegram:
		// ok
	default:
		return fmt.Errorf("news_jobs #%d (%s) has invalid sink %q (must be 'store' or 'telegram')", idx, name, nj.Sink)
	}

	if sink == NewsSinkTelegram {
		if tg == nil || !tg.Enabled() {
			return fmt.Errorf("news_jobs #%d (%s) sink 'telegram' requires telegram.enabled: true in config.yaml", idx, name)
		}
		if nj.TelegramChatID == 0 {
			return fmt.Errorf("news_jobs #%d (%s) sink 'telegram' requires telegram_chat_id to be set", idx, name)
		}
		if tg != nil && !tg.IsAllowedChatID(nj.TelegramChatID) {
			return fmt.Errorf("news_jobs #%d (%s) telegram_chat_id %d is not in telegram.allowed_chat_ids", idx, name, nj.TelegramChatID)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Scheduler.
// ---------------------------------------------------------------------------

// newsRunner is the seam between the scheduler and the news engine. The
// production wiring (cmd/onyx/main.go) supplies a closure that calls
// the real news.Orchestrator; tests supply a fake. Function type
// rather than struct — same pattern internal/telegram/runners.go uses
// for AgentRunner/ResearchRunner/NewsRunner.
//
// Returns the persisted store.NewsRun row plus the in-memory digest
// (for immediate Telegram delivery). The store is updated inside the
// runner closure via the same CreateNewsItems path the orchestrator
// uses, so a sink=store run leaves the same DB state as a sink=telegram
// run.
type newsRunner func(ctx context.Context, job NewsJobConfig) (*store.NewsRun, *news.NewsDigest, error)

// newsSink is the seam for delivery. Production uses NewTelegramSink;
// tests can supply a recorder. Always best-effort — a delivery failure
// is logged but does not flip the run status to failed.
//
// The signature carries the originating job config (not just the run)
// so the sink can resolve the per-job TelegramChatID without an
// extra DB lookup or a per-job sink set.
type newsSink func(ctx context.Context, job NewsJobConfig, run *store.NewsRun, digest *news.NewsDigest) error

// NewsRunnerFunc is the public alias of newsRunner, so callers in
// other packages can declare fields/returns of this type without
// reaching into the unexported name.
type NewsRunnerFunc = newsRunner

// NewsSinkFunc is the public alias of newsSink.
type NewsSinkFunc = newsSink

// Option configures a Scheduler instance.
type Option func(*Scheduler)

// WithLogger sets custom slog Logger for the scheduler.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Scheduler) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// WithAPIKey sets external API fallback key.
func WithAPIKey(apiKey string) Option {
	return func(s *Scheduler) {
		s.apiKey = apiKey
	}
}

// WithNewsRunner wires the news engine. Pass nil to disable news jobs
// (e.g. when no news_jobs are configured and you want to skip the
// orchestrator construction).
func WithNewsRunner(runner newsRunner) Option {
	return func(s *Scheduler) {
		s.newsRunner = runner
	}
}

// WithTelegramSink wires the Telegram delivery sink. Pass nil to
// disable Telegram delivery (the validator already rejects
// sink=telegram jobs when no sink is wired, so this should not happen
// in practice — but the nil-check keeps the per-tick loop safe).
func WithTelegramSink(sink newsSink) Option {
	return func(s *Scheduler) {
		s.tgSink = sink
	}
}

// Scheduler manages ticker-based recurring scrape + news jobs.
type Scheduler struct {
	config     *Config
	store      *store.Store
	client     *llm.Client
	logger     *slog.Logger
	apiKey     string
	newsRunner newsRunner
	tgSink     newsSink
}

// NewScheduler constructs a new Scheduler.
func NewScheduler(cfg *Config, st *store.Store, client *llm.Client, opts ...Option) *Scheduler {
	s := &Scheduler{
		config: cfg,
		store:  st,
		client: client,
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ---------------------------------------------------------------------------
// Scrape job execution (existing).
// ---------------------------------------------------------------------------

// RunJob executes a single scheduled scrape job once.
func (s *Scheduler) RunJob(ctx context.Context, job JobConfig) error {
	s.logger.Info("Starting scheduled job execution", "job", job.Name, "url", job.URL, "render", job.Render)

	rawHTML, usedBrowser, err := extract.FetchWithKey(ctx, s.apiKey, job.URL, job.Render)
	if err != nil {
		s.logger.Error("Scheduled job fetch failed", "job", job.Name, "url", job.URL, "error", err)
		return fmt.Errorf("job %s fetch error: %w", job.Name, err)
	}

	cleanText, err := extract.CleanHTML(rawHTML)
	if err != nil {
		s.logger.Error("Scheduled job clean HTML failed", "job", job.Name, "url", job.URL, "error", err)
		return fmt.Errorf("job %s clean HTML error: %w", job.Name, err)
	}

	var pageID int64
	if s.store != nil {
		if pid, err := s.store.SavePage(job.URL, rawHTML, cleanText, "scheduler", "ok"); err != nil {
			s.logger.Warn("Failed to save page to database during scheduled run", "job", job.Name, "url", job.URL, "error", err)
		} else {
			pageID = pid
			s.logger.Info("Saved scheduled scrape page to storage", "job", job.Name, "page_id", pageID)
		}
	}

	if job.Schema != "" && s.client != nil && s.store != nil && pageID > 0 {
		s.logger.Info("Extracting structured JSON for scheduled job", "job", job.Name, "schema", job.Schema)
		rawJSON, err := extract.ExtractJSON(ctx, s.client, rawHTML, job.Schema)
		if err != nil {
			s.logger.Error("Structured extraction failed for scheduled job", "job", job.Name, "schema", job.Schema, "error", err)
		} else {
			extID, err := s.store.SaveExtraction(pageID, job.Schema, string(rawJSON))
			if err != nil {
				s.logger.Warn("Failed to save extraction to database during scheduled run", "job", job.Name, "error", err)
			} else {
				s.logger.Info("Saved extraction to database", "job", job.Name, "extraction_id", extID)
			}
		}
	}

	s.logger.Info("Successfully completed scheduled job", "job", job.Name, "url", job.URL, "used_browser", usedBrowser)
	return nil
}

// ---------------------------------------------------------------------------
// News job execution (Phase 10).
// ---------------------------------------------------------------------------

// RunNewsJob executes a single scheduled news job once. The
// orchestrator is invoked through the newsRunner seam; on success the
// configured sink is invoked best-effort.
//
// RunNewsJob is safe to call from tests directly — it does not touch
// the global ticker or any package-level state.
func (s *Scheduler) RunNewsJob(ctx context.Context, job NewsJobConfig) error {
	if s.newsRunner == nil {
		return fmt.Errorf("news_jobs: %s: no news runner configured (call scheduler.WithNewsRunner)", job.Name)
	}

	sink := NewsJobSink(strings.ToLower(strings.TrimSpace(string(job.Sink))))
	if sink == "" {
		sink = NewsSinkStore
	}

	s.logger.Info("Starting scheduled news job", "job", job.Name, "window", job.Window, "profile", job.Profile, "sink", sink, "field_filter", job.Field)

	run, digest, err := s.newsRunner(ctx, job)
	if err != nil {
		// Run status was already updated by the orchestrator (or by
		// the test seam) before it returned the error. We just log
		// and return — the next tick will retry the whole pipeline.
		s.logger.Error("Scheduled news job failed", "job", job.Name, "error", err)
		return fmt.Errorf("news job %s: %w", job.Name, err)
	}

	if run == nil {
		return fmt.Errorf("news job %s: runner returned nil run with no error", job.Name)
	}

	s.logger.Info("Scheduled news job completed", "job", job.Name, "run_id", run.ID, "status", run.Status, "fields", digestFields(digest))

	// Best-effort delivery. The digest is already persisted in the
	// store regardless of sink choice; a delivery failure should not
	// flip the run's status or block the next tick.
	if sink == NewsSinkTelegram {
		if s.tgSink == nil {
			s.logger.Warn("news job: sink=telegram but no telegram sink configured; digest persisted to store only", "job", job.Name, "run_id", run.ID)
			return nil
		}
		if err := s.tgSink(ctx, job, run, digest); err != nil {
			s.logger.Error("news job: telegram delivery failed; digest is persisted in store and can be re-delivered later", "job", job.Name, "run_id", run.ID, "error", err)
			return nil
		}
		s.logger.Info("news job: telegram delivery complete", "job", job.Name, "run_id", run.ID, "chat_id", job.TelegramChatID)
	}

	return nil
}

// digestFields safely counts fields in a possibly-nil digest. Used
// only for log lines.
func digestFields(d *news.NewsDigest) int {
	if d == nil {
		return 0
	}
	return len(d.Fields)
}

// ---------------------------------------------------------------------------
// Telegram sink — production wiring for sink=telegram.
//
// Reuses the same chunker the /news command uses in the gateway (4096
// cap → ~4000 char splits at paragraph/newline/word boundaries). HTML
// parse mode is set so the bold/italic tags from
// news.FormatTelegramField render correctly. Delivery is best-effort;
// a send failure for one chunk is logged and the loop continues to
// the next chunk (matches the gateway's "one field's bad send should
// not abort the rest" rule from runners.go).
// ---------------------------------------------------------------------------

// NewTelegramSink returns a newsSink that delivers the per-field
// digest body to the chat id specified on each job. Header line is
// sent first (news.FormatTelegramDigestHeader) followed by one
// message sequence per field, each chunked to fit the Telegram
// message cap.
//
// Returning an error from this function means the header send failed
// OR every field failed to send; partial-success returns nil and logs
// the per-chunk failures.
func NewTelegramSink(bot *tgbotapi.BotAPI) newsSink {
	return func(ctx context.Context, job NewsJobConfig, run *store.NewsRun, digest *news.NewsDigest) error {
		if bot == nil {
			return fmt.Errorf("telegram sink: bot is nil")
		}
		chatID := job.TelegramChatID
		if chatID == 0 {
			return fmt.Errorf("telegram sink: job %s has telegram_chat_id=0 (LoadConfig should have rejected this)", job.Name)
		}
		if digest == nil {
			return sendMessage(bot, chatID, "📰 news run completed but no digest was produced.")
		}
		if len(digest.Fields) == 0 {
			header := news.FormatTelegramDigestHeader(digest)
			if err := sendMessage(bot, chatID, header); err != nil {
				return err
			}
			return sendMessage(bot, chatID, "No profile fields were processed. Check your profile configuration at /ui/profile.")
		}

		// Header
		header := news.FormatTelegramDigestHeader(digest)
		if err := sendHTML(bot, chatID, header); err != nil {
			return fmt.Errorf("telegram sink: header send failed: %w", err)
		}

		// Per-field, with 4000-char chunking.
		for _, fd := range digest.Fields {
			body := news.FormatTelegramField(fd, digest.Window)
			for _, chunk := range chunkMessage(body) {
				if err := sendHTML(bot, chatID, chunk); err != nil {
					// Log + continue; partial delivery beats total
					// failure for a recurring job.
					slog.Warn("telegram sink: chunk send failed; continuing", "chat_id", chatID, "field", fd.FieldName, "error", err)
				}
			}
		}
		return nil
	}
}

func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) error {
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := bot.Send(msg)
	return err
}

func sendHTML(bot *tgbotapi.BotAPI, chatID int64, html string) error {
	if html == "" {
		return nil
	}
	msg := tgbotapi.NewMessage(chatID, html)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.DisableWebPagePreview = true
	if _, err := bot.Send(msg); err != nil {
		return err
	}
	return nil
}

// chunkMessage splits `body` into <= 4000-char slices, preferring
// paragraph (\n\n) → newline → word boundaries. Mirrors
// internal/telegram/runners.go:chunkMessage exactly so the surface
// output is identical to the /news command.
//
// Inlined here (rather than imported) to keep internal/scheduler
// from depending on internal/telegram — the scheduler may run without
// the gateway.
func chunkMessage(body string) []string {
	const maxChunk = 4000
	body = strings.TrimSpace(body)
	if body == "" {
		return []string{"(no result)"}
	}
	if len(body) <= maxChunk {
		return []string{body}
	}

	var chunks []string
	remaining := body
	for len(remaining) > 0 {
		if len(remaining) <= maxChunk {
			chunks = append(chunks, remaining)
			break
		}
		cut := maxChunk
		if idx := strings.LastIndex(remaining[:maxChunk], "\n\n"); idx > 0 {
			cut = idx
		} else if idx := strings.LastIndex(remaining[:maxChunk], "\n"); idx > 0 {
			cut = idx
		} else if idx := strings.LastIndex(remaining[:maxChunk], " "); idx > 0 {
			cut = idx
		}
		chunks = append(chunks, strings.TrimSpace(remaining[:cut]))
		remaining = strings.TrimSpace(remaining[cut:])
	}
	return chunks
}

// ---------------------------------------------------------------------------
// Ticker lifecycle.
// ---------------------------------------------------------------------------

// Start launches background goroutines for all configured jobs.
// Scrape jobs and news jobs run in independent ticker goroutines; a
// crash or hang in one does not affect the other. Blocks until ctx is
// done.
func (s *Scheduler) Start(ctx context.Context) error {
	if s.config == nil {
		s.logger.Warn("No schedule config provided")
		return nil
	}

	scrapeCount := len(s.config.Jobs)
	newsCount := len(s.config.NewsJobs)

	if scrapeCount == 0 && newsCount == 0 {
		s.logger.Warn("No scheduled jobs configured to run")
		return nil
	}

	s.logger.Info("Starting scheduler service", "scrape_jobs", scrapeCount, "news_jobs", newsCount)

	var wg sync.WaitGroup

	// Scrape job tickers (Phase 1 behaviour — unchanged).
	for _, job := range s.config.Jobs {
		dur, err := time.ParseDuration(job.Interval)
		if err != nil {
			s.logger.Error("Skipping job with invalid interval", "job", job.Name, "interval", job.Interval, "error", err)
			continue
		}

		wg.Add(1)
		go func(j JobConfig, d time.Duration) {
			defer wg.Done()

			s.logger.Info("Initializing scrape job ticker", "job", j.Name, "interval", d.String())

			// Run immediately upon starting
			if err := s.RunJob(ctx, j); err != nil {
				s.logger.Error("Initial scrape job execution failed", "job", j.Name, "error", err)
			}

			ticker := time.NewTicker(d)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					s.logger.Info("Stopping scrape job ticker", "job", j.Name)
					return
				case <-ticker.C:
					if err := s.RunJob(ctx, j); err != nil {
						s.logger.Error("Scheduled scrape job ticker execution failed", "job", j.Name, "error", err)
					}
				}
			}
		}(job, dur)
	}

	// News job tickers (Phase 10).
	for _, nj := range s.config.NewsJobs {
		dur, err := time.ParseDuration(nj.Interval)
		if err != nil {
			// Should be unreachable — LoadConfig validates this.
			// Log + skip so a corrupt in-memory config doesn't crash.
			s.logger.Error("Skipping news job with invalid interval", "job", nj.Name, "interval", nj.Interval, "error", err)
			continue
		}

		wg.Add(1)
		go func(j NewsJobConfig, d time.Duration) {
			defer wg.Done()

			s.logger.Info("Initializing news job ticker", "job", j.Name, "interval", d.String(), "window", j.Window, "sink", j.Sink)

			// Run immediately upon starting, then on every tick.
			if err := s.RunNewsJob(ctx, j); err != nil {
				s.logger.Error("Initial news job execution failed", "job", j.Name, "error", err)
			}

			ticker := time.NewTicker(d)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					s.logger.Info("Stopping news job ticker", "job", j.Name)
					return
				case <-ticker.C:
					if err := s.RunNewsJob(ctx, j); err != nil {
						s.logger.Error("Scheduled news job ticker execution failed", "job", j.Name, "error", err)
					}
				}
			}
		}(nj, dur)
	}

	<-ctx.Done()
	s.logger.Info("Scheduler context cancelled, waiting for jobs to wrap up...")
	wg.Wait()
	s.logger.Info("All scheduler workers stopped cleanly")
	return nil
}

// end of file
