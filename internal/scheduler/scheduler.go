package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/kaiizer-99/onyx-scrapper/internal/extract"
	"github.com/kaiizer-99/onyx-scrapper/internal/llm"
	"github.com/kaiizer-99/onyx-scrapper/internal/store"
	"gopkg.in/yaml.v3"
)

// JobConfig defines a scheduled scraping job.
type JobConfig struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Interval string `yaml:"interval"`
	Render   bool   `yaml:"render"`
	Schema   string `yaml:"schema"`
}

// Config holds all scheduled job configurations.
type Config struct {
	Jobs []JobConfig `yaml:"jobs"`
}

// LoadConfig reads and parses a schedule configuration file.
func LoadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read schedule config file %s: %w", filePath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse schedule config YAML: %w", err)
	}

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

	return &cfg, nil
}

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

// Scheduler manages ticker-based recurring scrape jobs.
type Scheduler struct {
	config *Config
	store  *store.Store
	client *llm.Client
	logger *slog.Logger
	apiKey string
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
		pid, err := s.store.SavePage(job.URL, rawHTML, cleanText)
		if err != nil {
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

// Start launches background goroutines for all configured jobs.
// It blocks until ctx is done.
func (s *Scheduler) Start(ctx context.Context) error {
	if s.config == nil || len(s.config.Jobs) == 0 {
		s.logger.Warn("No scheduled jobs configured to run")
		return nil
	}

	s.logger.Info("Starting scheduler service", "job_count", len(s.config.Jobs))

	var wg sync.WaitGroup

	for _, job := range s.config.Jobs {
		dur, err := time.ParseDuration(job.Interval)
		if err != nil {
			s.logger.Error("Skipping job with invalid interval", "job", job.Name, "interval", job.Interval, "error", err)
			continue
		}

		wg.Add(1)
		go func(j JobConfig, d time.Duration) {
			defer wg.Done()

			s.logger.Info("Initializing job ticker", "job", j.Name, "interval", d.String())

			// Run immediately upon starting
			if err := s.RunJob(ctx, j); err != nil {
				s.logger.Error("Initial job execution failed", "job", j.Name, "error", err)
			}

			ticker := time.NewTicker(d)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					s.logger.Info("Stopping job ticker", "job", j.Name)
					return
				case <-ticker.C:
					if err := s.RunJob(ctx, j); err != nil {
						s.logger.Error("Scheduled job ticker execution failed", "job", j.Name, "error", err)
					}
				}
			}
		}(job, dur)
	}

	<-ctx.Done()
	s.logger.Info("Scheduler context cancelled, waiting for jobs to wrap up...")
	wg.Wait()
	s.logger.Info("All scheduler workers stopped cleanly")
	return nil
}
