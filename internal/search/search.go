package search

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kaiizer-99/onyx-scrapper/internal/store"
)

// SearchResult represents a normalized web or local search result item.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchResponse represents the top-level search response structure.
type SearchResponse struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

// Service provides web search and local fallback capabilities.
type Service struct {
	client *SearXNGClient
	store  *store.Store
}

// Option configures Service parameters.
type Option func(*Service)

// WithSearXNGURL sets a custom SearXNG instance URL.
func WithSearXNGURL(rawURL string) Option {
	return func(s *Service) {
		if rawURL != "" {
			s.client.baseURL = strings.TrimSuffix(rawURL, "/")
		}
	}
}

// WithHTTPClient sets a custom HTTP client for searching.
func WithHTTPClient(client *http.Client) Option {
	return func(s *Service) {
		if client != nil {
			s.client.httpClient = client
		}
	}
}

// NewService constructs a new Search Service.
func NewService(st *store.Store, opts ...Option) *Service {
	s := &Service{
		client: NewSearXNGClient("http://localhost:8888", &http.Client{Timeout: 5 * time.Second}),
		store:  st,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Search performs web search via SearXNG, falling back to local SQLite FTS5 store if SearXNG is unavailable.
func (s *Service) Search(ctx context.Context, query string) (*SearchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return &SearchResponse{
			Query:   "",
			Results: []SearchResult{},
		}, nil
	}

	// 1. Attempt SearXNG query
	results, err := s.client.Search(ctx, query)
	if err == nil && len(results) > 0 {
		return &SearchResponse{
			Query:   query,
			Results: results,
		}, nil
	}

	// 2. Fallback to SQLite FTS5 local database if available
	if s.store != nil {
		storeResults, storeErr := s.store.SearchPages(query)
		if storeErr == nil && len(storeResults) > 0 {
			converted := make([]SearchResult, 0, len(storeResults))
			for _, item := range storeResults {
				title := item.URL
				if parsed, pErr := url.Parse(item.URL); pErr == nil && parsed.Host != "" {
					title = parsed.Host + parsed.Path
				}
				converted = append(converted, SearchResult{
					Title:   title,
					URL:     item.URL,
					Snippet: item.Snippet,
				})
			}
			return &SearchResponse{
				Query:   query,
				Results: converted,
			}, nil
		}
	}

	// Return empty results if both SearXNG and local storage return no matches
	return &SearchResponse{
		Query:   query,
		Results: []SearchResult{},
	}, nil
}

