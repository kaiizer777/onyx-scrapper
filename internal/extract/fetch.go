package extract

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/kaiizer-99/onyx-scrapper/internal/browser"
)

// FetchStatic fetches static HTML from targetURL using colly HTTP collector.
func FetchStatic(ctx context.Context, targetURL string) (string, error) {
	c := colly.NewCollector(
		colly.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
	)
	c.SetRequestTimeout(15 * time.Second)

	var htmlContent string
	var fetchErr error

	c.OnResponse(func(r *colly.Response) {
		htmlContent = string(r.Body)
	})

	c.OnError(func(r *colly.Response, err error) {
		if err != nil {
			fetchErr = fmt.Errorf("fetch error (status %d): %w", r.StatusCode, err)
		} else if r.StatusCode >= 400 {
			fetchErr = fmt.Errorf("fetch failed with HTTP status: %d", r.StatusCode)
		}
	})

	err := c.Visit(targetURL)
	if err != nil && fetchErr == nil {
		fetchErr = err
	}

	if fetchErr != nil {
		return "", fetchErr
	}

	if htmlContent == "" {
		return "", fmt.Errorf("received empty content from %s", targetURL)
	}

	return htmlContent, nil
}

// Fetch returns HTML for targetURL. If forceRender is true, it uses go-rod headless browser.
// Otherwise it attempts static fetch first and falls back to go-rod browser if the static HTML
// appears empty or JS-rendered (e.g. sparse body text / SPA mount tags).
func Fetch(ctx context.Context, targetURL string, forceRender bool) (string, bool, error) {
	if forceRender {
		html, err := browser.FetchRendered(ctx, targetURL, 30*time.Second)
		return html, true, err
	}

	staticHTML, err := FetchStatic(ctx, targetURL)
	if err == nil {
		cleanText, cleanErr := CleanHTML(staticHTML)
		if cleanErr == nil && isContentSufficient(staticHTML, cleanText) {
			return staticHTML, false, nil
		}
	}

	// Fallback to go-rod rendered fetch
	renderedHTML, err := browser.FetchRendered(ctx, targetURL, 30*time.Second)
	if err != nil {
		if staticHTML != "" {
			return staticHTML, false, nil
		}
		return "", false, err
	}

	return renderedHTML, true, nil
}

// isContentSufficient checks if static HTML text is substantial enough or if it's an unrendered SPA container.
func isContentSufficient(rawHTML string, cleanText string) bool {
	if len(strings.TrimSpace(cleanText)) < 150 {
		return false
	}
	lowerRaw := strings.ToLower(rawHTML)
	if (strings.Contains(lowerRaw, `<div id="root"></div>`) ||
		strings.Contains(lowerRaw, `<div id="app"></div>`) ||
		strings.Contains(lowerRaw, "you need to enable javascript")) && len(strings.TrimSpace(cleanText)) < 300 {
		return false
	}
	return true
}
