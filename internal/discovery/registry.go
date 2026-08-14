package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/browser"
	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

// Reranker defines the interface for reranking candidate documents.
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []string) ([]RankedDoc, error)
}

// Registry manages discovery providers for searching and fetching.
type Registry struct {
	searchProviders []SearchProvider
	fetchProviders  map[string]FetchProvider
	fetchPriority   []string
	reranker        Reranker
}

// NewRegistry creates a new registry.
func NewRegistry(searchProviders []SearchProvider, fetchProviders map[string]FetchProvider, fetchPriority []string, reranker Reranker) *Registry {
	if len(fetchPriority) == 0 {
		fetchPriority = []string{"colly", "rod", "tinyfish", "jina", "scraperapi"}
	}
	return &Registry{
		searchProviders: searchProviders,
		fetchProviders:  fetchProviders,
		fetchPriority:   fetchPriority,
		reranker:        reranker,
	}
}

// Search fans out to all enabled SearchProviders concurrently, merges results, and dedupes by normalized URL.
func (r *Registry) Search(ctx context.Context, query string) []SearchResult {
	query = rewriteStaleYearQuery(query)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var allResults []SearchResult

	for _, p := range r.searchProviders {
		wg.Add(1)
		go func(provider SearchProvider) {
			defer wg.Done()
			
			// 15 seconds per-provider timeout to ensure no single provider hangs the search
			pCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			
			// Request up to 10 results from each provider for a good mix
			res, err := provider.Search(pCtx, query, 10)
			if err != nil {
				// We don't fail the whole search if one provider fails, just log it or ignore
				return
			}
			
			mu.Lock()
			allResults = append(allResults, res...)
			mu.Unlock()
		}(p)
	}

	wg.Wait()

	return dedupeResults(allResults)
}

func dedupeResults(results []SearchResult) []SearchResult {
	seen := make(map[string]bool)
	var deduped []SearchResult

	for _, r := range results {
		normalized := normalizeURL(r.URL)
		if !seen[normalized] {
			seen[normalized] = true
			deduped = append(deduped, r)
		}
	}

	return deduped
}

func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	
	q := u.Query()
	trackingParams := []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "ref", "source"}
	for _, p := range trackingParams {
		q.Del(p)
	}
	u.RawQuery = q.Encode()
	
	// Reconstruct the URL without fragment
	u.Fragment = ""
	return u.String()
}

// Fetch tries providers in priority order.
func (r *Registry) Fetch(ctx context.Context, targetURL string, opts FetchOptions) (*PageContent, error) {
	cb := browser.DefaultCircuitBreaker

	var lastErr error
	for _, providerName := range r.fetchPriority {
		provider, ok := r.fetchProviders[providerName]
		if !ok {
			continue // Provider not enabled or doesn't exist
		}

		if cb.IsTripped(targetURL) && providerName != "scraperapi" && providerName != "tinyfish" && providerName != "jina" {
		    // If tripped, we skip colly and rod, and only allow fallback providers
		    continue
		}

		// Colly detects JS-required pages internally, but if it fails, it will return an error and we fall through.
		res, err := provider.Fetch(ctx, targetURL, opts)
		if err != nil {
			cb.RecordFailure(targetURL)
			lastErr = err
			// If it's the JS required error from Colly, we want to try the next provider (rod)
			// For any error, we just fall through to the next provider.
			continue
		}

		// Success!
		cb.RecordSuccess(targetURL)
		return res, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all fetch providers failed, last error: %w", lastErr)
	}
	return nil, fmt.Errorf("no fetch providers available")
}

// Rerank reranks the given documents using the configured reranker.
func (r *Registry) Rerank(ctx context.Context, query string, docs []string) ([]RankedDoc, error) {
	if r.reranker == nil {
		// Fallback
		var out []RankedDoc
		for i, d := range docs {
			out = append(out, RankedDoc{Index: i, Text: d, Score: 1.0})
		}
		return out, nil
	}
	return r.reranker.Rerank(ctx, query, docs)
}

var (
	fourDigitYearPattern       = regexp.MustCompile(`\b(20\d{2})\b`)
	recencyKeywordPattern      = regexp.MustCompile(`(?i)\b(latest|current|recent|newest|this year|now)\b`)
	historicalEventNounPattern = regexp.MustCompile(`(?i)^\s*(financial\s+crisis|crisis|crash|recession|depression|pandemic|covid|covid-19|election|elections|olympics|bubble|war|wars|earthquake|tsunami|flood|treaty|accord|act|amendment|scandal|disaster|tragedy|incident|revolution|protest|protests|summit|attack|attacks)\b`)
	historicalPrepPattern      = regexp.MustCompile(`(?i)\b(in\s+the|of\s+the|during(\s+the)?|since(\s+the)?|before(\s+the)?|after(\s+the)?|about\s+the|on\s+the|throughout\s+the)\s+$`)
)

func rewriteStaleYearQuery(query string) string {
	if !recencyKeywordPattern.MatchString(query) {
		return query
	}
	currentYear := timecontext.Now().Year()
	rewritten := query
	changed := false

	locs := fourDigitYearPattern.FindAllStringIndex(query, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		loc := locs[i]
		start, end := loc[0], loc[1]
		match := query[start:end]

		prefix := query[:start]
		suffix := query[end:]

		if historicalEventNounPattern.MatchString(suffix) {
			continue
		}
		if historicalPrepPattern.MatchString(prefix) {
			continue
		}

		if year, err := strconv.Atoi(match); err == nil {
			if year < currentYear-1 {
				rewritten = rewritten[:start] + strconv.Itoa(currentYear) + rewritten[end:]
				changed = true
			}
		}
	}

	if changed {
		slog.Info("query_year_corrected", "original", query, "corrected", rewritten)
	}
	return rewritten
}
