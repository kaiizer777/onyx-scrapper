package discovery

import (
	"context"
	"errors"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/browser"
	"github.com/kaiizer777/onyx-scrapper/internal/extract"
	"github.com/kaiizer777/onyx-scrapper/internal/search"
)

var ErrProviderUnavailable = errors.New("provider unavailable")

type SearchResult struct {
	URL, Title, Snippet string
	Provider            string
}

type PageContent struct {
	URL, CleanText, RawHTML string
	Provider                string
	FetchedAt               time.Time
}

type FetchOptions struct {
	ForceRender bool
	Timeout     time.Duration
}

type SearchProvider interface {
	Name() string
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}

type FetchProvider interface {
	Name() string
	Fetch(ctx context.Context, url string, opts FetchOptions) (*PageContent, error)
}

// SearXNGProvider wraps the existing SearXNG search client.
type SearXNGProvider struct {
	client *search.SearXNGClient
}

func NewSearXNGProvider(client *search.SearXNGClient) *SearXNGProvider {
	return &SearXNGProvider{client: client}
}

func (p *SearXNGProvider) Name() string {
	return "searxng"
}

func (p *SearXNGProvider) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	results, err := p.client.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	
	var out []SearchResult
	for i, r := range results {
		if limit > 0 && i >= limit {
			break
		}
		out = append(out, SearchResult{
			URL:      r.URL,
			Title:    r.Title,
			Snippet:  r.Snippet,
			Provider: p.Name(),
		})
	}
	return out, nil
}

// CollyProvider wraps the static colly fetcher.
type CollyProvider struct{}

func NewCollyProvider() *CollyProvider {
	return &CollyProvider{}
}

func (p *CollyProvider) Name() string {
	return "colly"
}

func (p *CollyProvider) Fetch(ctx context.Context, url string, opts FetchOptions) (*PageContent, error) {
	rawHTML, err := extract.FetchStatic(ctx, url)
	if err != nil {
		return nil, err
	}
	cleanText, _ := extract.CleanHTML(rawHTML)
	return &PageContent{
		URL:       url,
		CleanText: cleanText,
		RawHTML:   rawHTML,
		Provider:  p.Name(),
		FetchedAt: time.Now(),
	}, nil
}

// RodProvider wraps the headless go-rod fetcher.
type RodProvider struct {
	pool *browser.Pool
}

func NewRodProvider(pool *browser.Pool) *RodProvider {
	return &RodProvider{pool: pool}
}

func (p *RodProvider) Name() string {
	return "rod"
}

func (p *RodProvider) Fetch(ctx context.Context, url string, opts FetchOptions) (*PageContent, error) {
	var rawHTML string
	var err error
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	if p.pool != nil {
		rawHTML, err = p.pool.FetchRendered(ctx, url, timeout)
	} else {
		rawHTML, err = browser.FetchRendered(ctx, url, timeout)
	}

	if err != nil {
		return nil, err
	}

	cleanText, _ := extract.CleanHTML(rawHTML)
	return &PageContent{
		URL:       url,
		CleanText: cleanText,
		RawHTML:   rawHTML,
		Provider:  p.Name(),
		FetchedAt: time.Now(),
	}, nil
}

// ScraperAPIProvider wraps the fallback scraperapi fetcher.
type ScraperAPIProvider struct {
	apiKey string
}

func NewScraperAPIProvider(apiKey string) *ScraperAPIProvider {
	return &ScraperAPIProvider{apiKey: apiKey}
}

func (p *ScraperAPIProvider) Name() string {
	return "scraperapi"
}

func (p *ScraperAPIProvider) Fetch(ctx context.Context, url string, opts FetchOptions) (*PageContent, error) {
	rawHTML, err := browser.FetchViaFallback(ctx, p.apiKey, url, opts.ForceRender)
	if err != nil {
		return nil, err
	}
	cleanText, _ := extract.CleanHTML(rawHTML)
	return &PageContent{
		URL:       url,
		CleanText: cleanText,
		RawHTML:   rawHTML,
		Provider:  p.Name(),
		FetchedAt: time.Now(),
	}, nil
}
