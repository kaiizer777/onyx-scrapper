package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/extract"
)

type TinyFishProvider struct {
	apiKey     string
	searchURL  string
	fetchURL   string
	httpClient *http.Client
}

func NewTinyFishProvider(apiKey string) *TinyFishProvider {
	return &TinyFishProvider{
		apiKey:    apiKey,
		searchURL: "https://api.search.tinyfish.ai",
		fetchURL:  "https://api.fetch.tinyfish.ai",
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (p *TinyFishProvider) Name() string {
	return "tinyfish"
}

type tinyFishSearchResponse struct {
	Results []struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

func (p *TinyFishProvider) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	reqURL := fmt.Sprintf("%s?query=%s", p.searchURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrProviderUnavailable
	}

	var searchResp tinyFishSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, ErrProviderUnavailable
	}

	var out []SearchResult
	for i, r := range searchResp.Results {
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

type tinyFishFetchRequest struct {
	URLs []string `json:"urls"`
}

type tinyFishFetchResponse struct {
	Results []struct {
		URL      string `json:"url"`
		Markdown string `json:"markdown"`
		HTML     string `json:"html"`
		Text     string `json:"text"`
	} `json:"results"`
}

func (p *TinyFishProvider) Fetch(ctx context.Context, targetURL string, opts FetchOptions) (*PageContent, error) {
	fetchReq := tinyFishFetchRequest{URLs: []string{targetURL}}
	body, err := json.Marshal(fetchReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.fetchURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	// Apply custom timeout if specified, capped by client timeout implicitly though
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
		req = req.WithContext(ctx)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrProviderUnavailable
	}

	var fetchResp tinyFishFetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&fetchResp); err != nil {
		return nil, ErrProviderUnavailable
	}

	if len(fetchResp.Results) == 0 {
		return nil, ErrProviderUnavailable
	}

	res := fetchResp.Results[0]
	rawHTML := res.HTML
	if rawHTML == "" {
		rawHTML = res.Markdown
	}
	if rawHTML == "" {
		rawHTML = res.Text
	}

	cleanText, _ := extract.CleanHTML(rawHTML)
	if cleanText == "" {
		cleanText = res.Text
	}

	return &PageContent{
		URL:       targetURL,
		CleanText: cleanText,
		RawHTML:   rawHTML,
		Provider:  p.Name(),
		FetchedAt: time.Now(),
	}, nil
}
