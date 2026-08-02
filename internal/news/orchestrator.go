package news

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	discoverypkg "github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/profile"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/search"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)


const (
	DefaultArticlesPerField    = 5
	DefaultMinArticlesBackfill = 3
	DefaultWorkerConcurrency   = 3
)

type FetchedFieldNews struct {
	Field store.ProfileField `json:"field"`
	Items []store.NewsItem   `json:"items"`
}

type Orchestrator struct {
	store                 *store.Store
	profileMgr            *profile.Manager
	registry              *discoverypkg.Registry
	searxng               *search.SearXNGClient
	authManager           *quality.AuthorityManager
	budget                *quality.Budget
	httpClient            *http.Client
	llmClient             *llm.Client
	reranker              *discoverypkg.JinaReranker
	articlesPerField      int
	minArticlesBackfill   int
	maxFields             int
	maxArticlesPerField   int
}

// ErrTooManyFields is returned by Run / RunWithOptions when the
// profile's enabled-field count exceeds the configured MaxFields
// cap. Surfaces as a 400 in the HTTP gateway and a friendly error
// in the CLI; not retried by the orchestrator.
var ErrTooManyFields = fmt.Errorf("news run aborted: profile field count exceeds configured cap")

func NewOrchestrator(
	st *store.Store,
	profMgr *profile.Manager,
	registry *discoverypkg.Registry,
	searxng *search.SearXNGClient,
	cfg *config.Config,
) *Orchestrator {
	articlesPerField := DefaultArticlesPerField
	minBackfill := DefaultMinArticlesBackfill
	// Per-run ceiling on full-text fetches. Each article the
	// news digest surfaces needs a real body — that's the whole
	// point of the feature. The previous default of 40 was
	// exhausted before every item got pulled once the
	// profile had 2-3 enabled fields with the default 5
	// articles per field, leaving a wall of RSS snippets in
	// the rendered report. We bumped to 120 so a default run
	// (3 fields × 5 items = 15 fetches) fits comfortably and
	// even a larger profile (5 fields × 10 items = 50 fetches)
	// still has headroom. Operators who want to tighten the
	// ceiling can set quality.max_extra_calls_per_run.
	maxExtraCalls := 120
	maxFields, maxArticlesPerField := config.DefaultNewsMaxFields, config.DefaultNewsMaxArticlesPerField

	if cfg != nil {
		if cfg.News != nil {
			if cfg.News.ArticlesPerField > 0 {
				articlesPerField = cfg.News.ArticlesPerField
			}
			if cfg.News.MinArticlesBackfill > 0 {
				minBackfill = cfg.News.MinArticlesBackfill
			}
		}
		maxFields, maxArticlesPerField = cfg.ResolveNewsCaps()
		if cfg.Quality != nil && cfg.Quality.MaxExtraCallsPerRun > 0 {
			maxExtraCalls = cfg.Quality.MaxExtraCallsPerRun
		}
	}

	// Per-field article cap is the lesser of the configured default
	// and the hard cap. This is the second-line guardrail: the
	// dominant cost ceiling is the quality.Budget for full-text
	// pulls, but this caps the RSS-flood surface for a single
	// field independently.
	if articlesPerField > maxArticlesPerField {
		articlesPerField = maxArticlesPerField
	}

	budget := quality.NewBudget(maxExtraCalls)

	var authManager *quality.AuthorityManager
	if cfg != nil && cfg.Quality != nil && (cfg.Quality.SourceAuthority.Enabled == nil || *cfg.Quality.SourceAuthority.Enabled) {
		authManager = quality.NewAuthorityManager()
		tiersPath := cfg.Quality.SourceAuthority.TiersConfigPath
		if tiersPath == "" {
			tiersPath = "config/authority_tiers.yaml"
		}
		if err := authManager.LoadTiers(tiersPath); err != nil {
			slog.Warn("Failed to load authority tiers in news orchestrator", "error", err)
		}
	}

	return &Orchestrator{
		store:               st,
		profileMgr:          profMgr,
		registry:            registry,
		searxng:             searxng,
		authManager:         authManager,
		budget:              budget,
		httpClient:          &http.Client{Timeout: 15 * time.Second},
		articlesPerField:    articlesPerField,
		minArticlesBackfill: minBackfill,
		maxFields:           maxFields,
		maxArticlesPerField: maxArticlesPerField,
	}
}

// Run executes the news fetch pipeline for all enabled fields of the specified profile (or default profile if profileID==0).
func (o *Orchestrator) Run(ctx context.Context, win Window, profileID int64) (*store.NewsRun, []FetchedFieldNews, error) {
	return o.RunWithOptions(ctx, win, profileID, "")
}

// RunWithPreCreatedRun executes the fetch pipeline using an already-created NewsRun.
// The caller must have created the run via store.CreateNewsRun first so the HTTP
// handler can return the run_id in a 202 before the run completes.
func (o *Orchestrator) RunWithPreCreatedRun(ctx context.Context, newsRun *store.NewsRun, win Window, fieldFilter string) (*store.NewsRun, []FetchedFieldNews, error) {
	fields, err := o.store.ListEnabledProfileFields(newsRun.ProfileID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch enabled profile fields: %w", err)
	}
	if fieldFilter != "" {
		var filtered []store.ProfileField
		for _, f := range fields {
			if strings.EqualFold(f.FieldName, fieldFilter) {
				filtered = append(filtered, f)
			}
		}
		if len(filtered) == 0 {
			return nil, nil, fmt.Errorf("field %q not found or disabled in profile", fieldFilter)
		}
		fields = filtered
	}
	if len(fields) == 0 {
		return nil, nil, fmt.Errorf("no enabled profile fields configured for run profile %d", newsRun.ProfileID)
	}

	// Phase 11: cost guardrail. Reject the run up front if the
	// profile's enabled-field count exceeds the configured cap. We
	// refuse rather than truncate silently so a misconfigured
	// profile surfaces as a config error, not a half-fetched digest.
	if len(fields) > o.maxFields {
		_ = o.store.CompleteNewsRun(newsRun.ID, "rejected")
		slog.Warn("news_run_rejected: too many fields",
			"run_id", newsRun.ID,
			"profile_id", newsRun.ProfileID,
			"enabled_fields", len(fields),
			"max_fields_cap", o.maxFields,
		)
		return nil, nil, fmt.Errorf("%w: %d enabled > cap %d (lower the profile's enabled-field count or raise config.news.max_fields)", ErrTooManyFields, len(fields), o.maxFields)
	}

	slog.Info("Starting news run", "run_id", newsRun.ID, "profile_id", newsRun.ProfileID, "window", win.RawPhrase, "fields_count", len(fields))

	results := make([]FetchedFieldNews, len(fields))
	var wg sync.WaitGroup
	sem := make(chan struct{}, DefaultWorkerConcurrency)

	for i, field := range fields {
		wg.Add(1)
		go func(idx int, f store.ProfileField) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Phase 12: per-field worker isolation. A panic from
			// one field's fetch (e.g. a hostile upstream response,
			// a nil-deref in a discovery provider, a SearXNG
			// response shape change) MUST NOT take down the
			// other goroutines. The recover sits at the goroutine
			// boundary so the panic is contained; the field's
			// results slot stays at its zero value (empty items)
			// and the run continues with the surviving fields.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("news_field_worker_panic: contained panic in per-field worker",
						"run_id", newsRun.ID,
						"field_id", f.ID,
						"field_name", f.FieldName,
						"panic", fmt.Sprintf("%v", r),
					)
					// results[idx] is already its zero-value
					// FetchedFieldNews (Items: nil); the run
					// continues.
				}
			}()

			slog.Info("Fetching news for field", "run_id", newsRun.ID, "field", f.FieldName)
			fieldItems := o.fetchNewsForField(ctx, newsRun.ID, f, win)
			results[idx] = FetchedFieldNews{Field: f, Items: fieldItems}
		}(i, field)
	}

	wg.Wait()

	if ctx.Err() != nil {
		_ = o.store.CompleteNewsRun(newsRun.ID, "cancelled")
		return nil, nil, ctx.Err()
	}

	var allItems []store.NewsItem
	for _, res := range results {
		allItems = append(allItems, res.Items...)
	}

	if len(allItems) > 0 {
		inserted, iErr := o.store.CreateNewsItems(allItems)
		if iErr != nil {
			slog.Error("Failed to store news items", "run_id", newsRun.ID, "error", iErr)
		} else {
			itemMap := make(map[string]store.NewsItem)
			for _, it := range inserted {
				key := fmt.Sprintf("%d-%s", it.FieldID, it.URL)
				itemMap[key] = it
			}
			for i, res := range results {
				for j, item := range res.Items {
					key := fmt.Sprintf("%d-%s", item.FieldID, item.URL)
					if updated, ok := itemMap[key]; ok {
						results[i].Items[j] = updated
					}
				}
			}
		}
	}

	_ = o.store.CompleteNewsRun(newsRun.ID, "completed")
	if completedRun, gErr := o.store.GetNewsRun(newsRun.ID); gErr == nil && completedRun != nil {
		newsRun = completedRun
	}
	slog.Info("News run completed", "run_id", newsRun.ID, "total_items", len(allItems))
	return newsRun, results, nil
}

// SummarizeAndReturnWithPreCreatedRun combines RunWithPreCreatedRun + Phase 5 summarization.
func (o *Orchestrator) SummarizeAndReturnWithPreCreatedRun(ctx context.Context, newsRun *store.NewsRun, win Window, fieldFilter string) (*store.NewsRun, *NewsDigest, error) {
	run, fetched, err := o.RunWithPreCreatedRun(ctx, newsRun, win, fieldFilter)
	if err != nil {
		return nil, nil, err
	}
	digest, err := o.SummarizeDigest(ctx, run, fetched)
	if err != nil {
		return run, nil, err
	}
	return run, digest, nil
}

// RunWithOptions executes the news fetch pipeline with optional single field filtering for testing/debugging.
func (o *Orchestrator) RunWithOptions(ctx context.Context, win Window, profileID int64, fieldFilter string) (*store.NewsRun, []FetchedFieldNews, error) {
	var targetProfile *store.UserProfile
	var err error

	if profileID > 0 {
		p, pErr := o.store.GetProfile(profileID)
		if pErr != nil || p == nil {
			return nil, nil, fmt.Errorf("profile not found: %d", profileID)
		}
		targetProfile = p
	} else {
		targetProfile, err = o.profileMgr.GetOrCreateDefaultProfile()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load default profile: %w", err)
		}
	}

	fields, err := o.store.ListEnabledProfileFields(targetProfile.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch enabled profile fields: %w", err)
	}
	if fieldFilter != "" {
		var filtered []store.ProfileField
		for _, f := range fields {
			if strings.EqualFold(f.FieldName, fieldFilter) {
				filtered = append(filtered, f)
			}
		}
		if len(filtered) == 0 {
			return nil, nil, fmt.Errorf("field %q not found or disabled in profile %q", fieldFilter, targetProfile.Name)
		}
		fields = filtered
	}
	if len(fields) == 0 {
		return nil, nil, fmt.Errorf("no enabled profile fields configured for profile %q", targetProfile.Name)
	}

	// Phase 11: cost guardrail. Reject the run up front if the
	// profile's enabled-field count exceeds the configured cap.
	// Mirrors the check in RunWithPreCreatedRun so both code paths
	// have identical safety posture.
	if len(fields) > o.maxFields {
		slog.Warn("news_run_rejected: too many fields",
			"profile_id", targetProfile.ID,
			"profile_name", targetProfile.Name,
			"enabled_fields", len(fields),
			"max_fields_cap", o.maxFields,
		)
		return nil, nil, fmt.Errorf("%w: %d enabled > cap %d (lower the profile's enabled-field count or raise config.news.max_fields)", ErrTooManyFields, len(fields), o.maxFields)
	}

	newsRun, err := o.store.CreateNewsRun(targetProfile.ID, win.RawPhrase)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create news run: %w", err)
	}

	slog.Info("Starting news run", "run_id", newsRun.ID, "profile_id", targetProfile.ID, "window", win.RawPhrase, "fields_count", len(fields))

	results := make([]FetchedFieldNews, len(fields))
	var wg sync.WaitGroup
	sem := make(chan struct{}, DefaultWorkerConcurrency)

	for i, field := range fields {
		wg.Add(1)
		go func(idx int, f store.ProfileField) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Phase 12: per-field worker isolation. Mirrors the
			// recover in RunWithPreCreatedRun — both code paths
			// must absorb a single field's panic so sibling
			// fields still get their results and the run is
			// still useful to the user. See RunWithPreCreatedRun
			// for the full rationale.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("news_field_worker_panic: contained panic in per-field worker",
						"run_id", newsRun.ID,
						"field_id", f.ID,
						"field_name", f.FieldName,
						"panic", fmt.Sprintf("%v", r),
					)
					// results[idx] stays at its zero value.
				}
			}()

			slog.Info("Fetching news for field", "run_id", newsRun.ID, "field", f.FieldName)
			fieldItems := o.fetchNewsForField(ctx, newsRun.ID, f, win)
			results[idx] = FetchedFieldNews{
				Field: f,
				Items: fieldItems,
			}
		}(i, field)
	}

	wg.Wait()

	if ctx.Err() != nil {
		_ = o.store.CompleteNewsRun(newsRun.ID, "cancelled")
		return nil, nil, ctx.Err()
	}

	// Persist all gathered news items
	var allItems []store.NewsItem
	for _, res := range results {
		allItems = append(allItems, res.Items...)
	}

	if len(allItems) > 0 {
		inserted, err := o.store.CreateNewsItems(allItems)
		if err != nil {
			slog.Error("Failed to store news items", "run_id", newsRun.ID, "error", err)
		} else {
			// Update result items with inserted IDs
			itemMap := make(map[string]store.NewsItem)
			for _, it := range inserted {
				key := fmt.Sprintf("%d-%s", it.FieldID, it.URL)
				itemMap[key] = it
			}
			for i, res := range results {
				for j, item := range res.Items {
					key := fmt.Sprintf("%d-%s", item.FieldID, item.URL)
					if updated, ok := itemMap[key]; ok {
						results[i].Items[j] = updated
					}
				}
			}
		}
	}

	_ = o.store.CompleteNewsRun(newsRun.ID, "completed")
	newsRun, _ = o.store.GetNewsRun(newsRun.ID)

	slog.Info("News run completed successfully", "run_id", newsRun.ID, "total_items", len(allItems))
	return newsRun, results, nil
}

func (o *Orchestrator) fetchNewsForField(ctx context.Context, runID int64, field store.ProfileField, win Window) []store.NewsItem {
	var candidateItems []store.NewsItem

	// 1. Google News RSS Query
	rssArticles, err := FetchGoogleNewsRSS(ctx, o.httpClient, field.KeywordsCSV, win.GoogleNewsWhen)
	if err != nil {
		slog.Warn("Google News RSS query failed for field", "field", field.FieldName, "error", err)
	} else {
		for _, art := range rssArticles {
			pub := art.PublishedAt
			item := store.NewsItem{
				RunID:          runID,
				FieldID:        field.ID,
				Title:          art.Title,
				URL:            art.URL,
				Source:         art.Source,
				Summary:        art.Snippet,
				FetchIntegrity: string(quality.FetchOK),
			}
			if !pub.IsZero() {
				item.PublishedAt = &pub
			}
			candidateItems = append(candidateItems, item)
		}
	}

	// 2. Cross-check / backfill if RSS returns thin results
	if len(candidateItems) < o.minArticlesBackfill {
		slog.Info("RSS returned thin results, executing backfill", "field", field.FieldName, "count", len(candidateItems), "target_min", o.minArticlesBackfill)
		backfilled := o.backfillNews(ctx, runID, field)
		candidateItems = append(candidateItems, backfilled...)
	}

	// 3. Deduplicate articles within field
	dedupedItems := o.deduplicateNewsItems(candidateItems)

	// 3a. Hard recency cutoff. Google News RSS accepts the `when:`
	// hint but does not always honor it (publisher metadata can
	// disagree with the article's own date, and backfill sources
	// may not honor it at all). We enforce the operator's window
	// here so an item published outside the window never reaches
	// the render. Items with an unknown PublishedAt (nil) are
	// kept — RSS feeds that don't ship a date still count, the
	// operator chose the window in good faith.
	dedupedItems = filterItemsByRecency(dedupedItems, win.Since, field.FieldName)

	// 4. Limit to articlesPerField, clamped to the per-field hard
	// cap. NewOrchestrator already applied the cap to
	// o.articlesPerField, but this clamp is a defensive no-op in
	// case a future caller mutates the orchestrator's field after
	// construction.
	limit := o.articlesPerField
	if limit > o.maxArticlesPerField {
		limit = o.maxArticlesPerField
	}
	if len(dedupedItems) > limit {
		dedupedItems = dedupedItems[:limit]
	}

	// 5. Full-article pull & quality check for every item.
	//
	// The Google News RSS <description> is typically just a
	// markdown link to the publisher plus a tiny "Source" tag
	// (e.g. "[Title](https://news.google.com/rss/articles/CBMi…)
	// Hartford Courant"). The whole point of the news feature
	// is to surface readable article bodies, not those link
	// stubs — so we now attempt the full-text pull for every
	// item (subject to the per-run quality budget). The
	// MinContentChars gate that previously skipped many items
	// is gone: an item whose RSS summary is short or whose
	// summary is just a markdown link with no real prose
	// should still get a real body. A pull that returns empty
	// or fails now leaves a short, explicit "no body
	// available" marker so the operator never sees a wall of
	// raw URLs in the rendered report.
	for i := range dedupedItems {
		item := &dedupedItems[i]

		if o.registry == nil {
			// No fetch registry wired in (some embedded /
			// test configurations). We can't pull full text;
			// fall through and let the snippet survive so the
			// article at least has a title + link.
			continue
		}
		if !o.budget.TryAcquire() {
			slog.Warn("Quality budget exhausted for news run, skipping full text pull", "url", item.URL)
			continue
		}
		fetchRes, fetchErr := o.registry.Fetch(ctx, item.URL, discoverypkg.FetchOptions{Timeout: 10 * time.Second})

		if fetchErr == nil && fetchRes != nil {
			cleanText := fetchRes.CleanText
			// Persist the full body so the Web UI / Telegram /
			// CLI can show the article text inline without
			// forcing the operator to click through. The
			// `summary` SQLite column is TEXT (no length cap)
			// and the orchestrator's own field-limit clamp
			// (o.articlesPerField, default 5 per field) keeps
			// the per-run body footprint bounded. We still
			// apply a defensive hard ceiling of 12 KiB of
			// clean text per item — a pathological site
			// shouldn't blow up a digest render.
			const maxBodyBytes = 12 * 1024
			if len(cleanText) > maxBodyBytes {
				cleanText = cleanText[:maxBodyBytes]
			}
			if strings.TrimSpace(cleanText) != "" {
				item.Summary = cleanText
			} else {
				// Pull succeeded but the page was empty
				// (paywall, soft-404, JS-only). Replace
				// the garbage RSS snippet with an explicit
				// "not available" marker so the rendered
				// report never shows a raw URL dump.
				item.Summary = bodyNotAvailableMarker(item)
			}
			integrity := quality.AnalyzeFetchIntegrity(fetchRes.RawHTML, cleanText, fetchRes.Provider, nil)
			item.FetchIntegrity = string(integrity)
		} else {
			integrity := quality.AnalyzeFetchIntegrity("", "", "", fetchErr)
			item.FetchIntegrity = string(integrity)
			// Fetch failed (network, bot-block, etc.).
			// Replace the RSS snippet (which is typically a
			// Google News tracking link with no real prose)
			// with the same explicit marker so the report
			// stays readable.
			item.Summary = bodyNotAvailableMarker(item)
		}
	}

	return dedupedItems
}

// bodyNotAvailableMarker returns a short, operator-friendly
// "body not available" line for an item whose full-text pull
// did not produce a usable article body. The text deliberately
// names the source so the operator knows which publisher to
// click through to, and it deliberately contains no URL syntax
// or markdown link cruft — the formatter's body-cleaning pass
// will pass it through untouched.
func bodyNotAvailableMarker(item *store.NewsItem) string {
	if item == nil {
		return "Full article text was not available for this headline. Click the source link below to read it on the publisher's site."
	}
	src := strings.TrimSpace(item.Source)
	if src == "" {
		return "Full article text was not available for this headline. Click the source link below to read it on the publisher's site."
	}
	return fmt.Sprintf("Full article text was not available for this headline. Click the source link below to read it on %s.", src)
}

func (o *Orchestrator) backfillNews(ctx context.Context, runID int64, field store.ProfileField) []store.NewsItem {
	var backfilled []store.NewsItem

	// Backfill Source A: SearXNG news category
	if o.searxng != nil {
		q := fmt.Sprintf("%s news", strings.ReplaceAll(field.KeywordsCSV, ",", " "))
		searxngRes, err := o.searxng.SearchCategory(ctx, q, "news")
		if err == nil {
			for _, r := range searxngRes {
				backfilled = append(backfilled, store.NewsItem{
					RunID:          runID,
					FieldID:        field.ID,
					Title:          r.Title,
					URL:            r.URL,
					Source:         "SearXNG News",
					Summary:        r.Snippet,
					FetchIntegrity: string(quality.FetchOK),
				})
			}
		}
	}

	// Backfill Source B: Registry discovery providers (e.g. TinyFish / SearXNG general)
	if len(backfilled) < o.minArticlesBackfill && o.registry != nil {
		q := field.KeywordsCSV
		regResults := o.registry.Search(ctx, q)
		for _, r := range regResults {
			backfilled = append(backfilled, store.NewsItem{
				RunID:          runID,
				FieldID:        field.ID,
				Title:          r.Title,
				URL:            r.URL,
				Source:         r.Provider,
				Summary:        r.Snippet,
				FetchIntegrity: string(quality.FetchOK),
			})
		}
	}

	return backfilled
}

func (o *Orchestrator) deduplicateNewsItems(items []store.NewsItem) []store.NewsItem {
	seenURL := make(map[string]bool)
	seenTitle := make(map[string]bool)
	var deduped []store.NewsItem

	for _, item := range items {
		normURL := normalizeNewsURL(item.URL)
		if normURL == "" || seenURL[normURL] {
			continue
		}

		normTitle := normalizeNewsTitle(item.Title)
		if normTitle == "" || seenTitle[normTitle] {
			continue
		}

		seenURL[normURL] = true
		seenTitle[normTitle] = true
		deduped = append(deduped, item)
	}

	return deduped
}

var trackingQueryParams = map[string]bool{
	"utm_source":   true,
	"utm_medium":   true,
	"utm_campaign": true,
	"utm_term":     true,
	"utm_content":  true,
	"ref":          true,
	"source":       true,
	"ved":          true,
	"usg":          true,
}

func normalizeNewsURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.Fragment = ""

	q := u.Query()
	for param := range q {
		if trackingQueryParams[strings.ToLower(param)] {
			q.Del(param)
		}
	}
	u.RawQuery = q.Encode()

	return u.String()
}

var nonAlphanumericRegex = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeNewsTitle(title string) string {
	lower := strings.ToLower(strings.TrimSpace(title))
	// Strip source suffix if present (e.g. "... - Reuters")
	if idx := strings.LastIndex(lower, " - "); idx != -1 {
		lower = lower[:idx]
	}
	cleaned := nonAlphanumericRegex.ReplaceAllString(lower, " ")
	return strings.TrimSpace(cleaned)
}

// GetQualityBudget returns the budget governor used for this news run.
func (o *Orchestrator) GetQualityBudget() *quality.Budget {
	return o.budget
}

// SetLLMClient configures the LLM client used for digest takeaway generation.
func (o *Orchestrator) SetLLMClient(client *llm.Client) {
	o.llmClient = client
}

// SetReranker configures the Jina reranker used for ordering news items.
func (o *Orchestrator) SetReranker(reranker *discoverypkg.JinaReranker) {
	o.reranker = reranker
}

// SummarizeDigest runs the Phase 5 summarization pass on fetched news items to produce a NewsDigest.
func (o *Orchestrator) SummarizeDigest(ctx context.Context, newsRun *store.NewsRun, fetched []FetchedFieldNews) (*NewsDigest, error) {
	if newsRun == nil {
		return nil, fmt.Errorf("cannot summarize nil news run")
	}
	summarizer := NewSummarizer(o.llmClient, o.reranker, o.authManager, o.budget, o.store)
	return summarizer.CompileDigest(ctx, newsRun, fetched)
}

// RunAndSummarize executes both the fetch phase (Phase 4) and summarization phase (Phase 5) in sequence.
func (o *Orchestrator) RunAndSummarize(ctx context.Context, win Window, profileID int64) (*store.NewsRun, *NewsDigest, error) {
	return o.RunAndSummarizeWithOptions(ctx, win, profileID, "")
}

// RunAndSummarizeWithOptions executes fetch and summarization with optional single field filtering.
func (o *Orchestrator) RunAndSummarizeWithOptions(ctx context.Context, win Window, profileID int64, fieldFilter string) (*store.NewsRun, *NewsDigest, error) {
	newsRun, fetched, err := o.RunWithOptions(ctx, win, profileID, fieldFilter)
	if err != nil {
		return nil, nil, err
	}
	digest, err := o.SummarizeDigest(ctx, newsRun, fetched)
	if err != nil {
		return newsRun, nil, err
	}
	return newsRun, digest, nil
}

// filterItemsByRecency drops any item whose PublishedAt falls
// strictly before `since`. Items with a nil PublishedAt are kept
// (an operator who chose a 5-day window shouldn't lose every
// article from a feed that doesn't ship dates). The function
// returns the original slice unchanged when `since` is the zero
// time so the orchestrator can call it unconditionally without
// needing a separate "do we have a real window?" branch.
//
// Exported so the view-build layer (digest_view.go) can apply
// the same rule when re-rendering a saved run; the orchestrator
// already filtered at fetch time, but post-run reads must
// honor the same contract.
func filterItemsByRecency(items []store.NewsItem, since time.Time, fieldName string) []store.NewsItem {
	if since.IsZero() || len(items) == 0 {
		return items
	}
	out := make([]store.NewsItem, 0, len(items))
	for _, it := range items {
		if it.PublishedAt != nil && it.PublishedAt.Before(since) {
			slog.Debug("dropping news item outside recency window",
				"field", fieldName,
				"url", it.URL,
				"published_at", it.PublishedAt,
				"since", since,
			)
			continue
		}
		out = append(out, it)
	}
	return out
}

