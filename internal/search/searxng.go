package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearXNGClient queries a local or remote SearXNG search engine instance.
type SearXNGClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewSearXNGClient constructs a new SearXNG client instance.
func NewSearXNGClient(baseURL string, client *http.Client) *SearXNGClient {
	if baseURL == "" {
		baseURL = "http://localhost:8888"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &SearXNGClient{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		httpClient: client,
	}
}

type searxngResponse struct {
	Query   string `json:"query"`
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

// Search executes a query against SearXNG and returns parsed SearchResult items.
func (c *SearXNGClient) Search(ctx context.Context, query string) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []SearchResult{}, nil
	}

	endpoint := fmt.Sprintf("%s/search?q=%s&format=json", c.baseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create searxng request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng returned status code %d", resp.StatusCode)
	}

	var payload searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode searxng response: %w", err)
	}

	results := make([]SearchResult, 0, len(payload.Results))
	for _, item := range payload.Results {
		snippet := item.Snippet
		if snippet == "" {
			snippet = item.Content
		}
		results = append(results, SearchResult{
			Title:   item.Title,
			URL:     item.URL,
			Snippet: snippet,
		})
	}

	return results, nil
}
