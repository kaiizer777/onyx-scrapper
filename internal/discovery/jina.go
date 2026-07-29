package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/extract"
	"golang.org/x/time/rate"
)

type JinaProvider struct {
	apiKey     string
	searchURL  string
	fetchURL   string
	httpClient *http.Client
	limiter    *rate.Limiter
}

func NewJinaProvider(apiKey string) *JinaProvider {
	limit := rate.Every(time.Minute / 18)
	burst := 1
	if apiKey != "" {
		limit = rate.Every(time.Minute / 500)
		burst = 5
	}

	return &JinaProvider{
		apiKey:     apiKey,
		searchURL:  "https://s.jina.ai/",
		fetchURL:   "https://r.jina.ai/",
		httpClient: &http.Client{Timeout: 15 * time.Second},
		limiter:    rate.NewLimiter(limit, burst),
	}
}

func (p *JinaProvider) Name() string {
	return "jina"
}

type jinaSearchResponse struct {
	Data []struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Content     string `json:"content"`
	} `json:"data"`
}

func (p *JinaProvider) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s%s", p.searchURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrProviderUnavailable
	}

	var searchResp jinaSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, ErrProviderUnavailable
	}

	var out []SearchResult
	for i, r := range searchResp.Data {
		if limit > 0 && i >= limit {
			break
		}
		snippet := r.Description
		if snippet == "" {
			snippet = r.Content
		}
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		out = append(out, SearchResult{
			URL:      r.URL,
			Title:    r.Title,
			Snippet:  snippet,
			Provider: p.Name(),
		})
	}

	return out, nil
}

func (p *JinaProvider) Fetch(ctx context.Context, targetURL string, opts FetchOptions) (*PageContent, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s%s", p.fetchURL, targetURL)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

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

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, ErrProviderUnavailable
	}

	markdown := string(bodyBytes)
	cleanText, _ := extract.CleanHTML(markdown)
	if cleanText == "" {
		cleanText = markdown
	}

	return &PageContent{
		URL:       targetURL,
		CleanText: cleanText,
		RawHTML:   markdown,
		Provider:  p.Name(),
		FetchedAt: time.Now(),
	}, nil
}
