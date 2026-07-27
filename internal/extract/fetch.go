package extract

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/kaiizer-99/onyx-scrapper/internal/browser"
	stealthpkg "github.com/kaiizer-99/onyx-scrapper/internal/stealth"
)

// FetchStatic fetches static HTML from targetURL using colly HTTP collector.
func FetchStatic(ctx context.Context, targetURL string) (string, error) {
	// 1. Enforce per-domain rate limiting
	if err := stealthpkg.DefaultDomainRateLimiter.Wait(ctx, targetURL); err != nil {
		slog.Warn("Rate limiter wait warning", "url", targetURL, "error", err)
	}

	// 2. Randomized stealth profile
	prof := stealthpkg.GetRandomProfile()

	// 3. Robots.txt check
	allowed, err := stealthpkg.DefaultRobotsChecker.IsAllowed(ctx, prof.UserAgent, targetURL)
	if err == nil && !allowed {
		slog.Warn("robots.txt disallows static fetching", "url", targetURL)
	}

	c := colly.NewCollector(
		colly.UserAgent(prof.UserAgent),
	)
	c.SetRequestTimeout(15 * time.Second)

	var htmlContent string
	var fetchErr error

	c.OnRequest(func(r *colly.Request) {
		r.Headers.Set("Accept-Language", prof.AcceptLanguage)
		r.Headers.Set("Sec-Ch-Ua", prof.SecChUa)
		r.Headers.Set("Sec-Ch-Ua-Mobile", prof.SecChUaMobile)
		r.Headers.Set("Sec-Ch-Ua-Platform", prof.SecChUaPlatform)
	})

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

	_ = stealthpkg.HumanDelayCtx(ctx, 300, 800)

	err = c.Visit(targetURL)
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
// appears empty or JS-rendered. If direct fetch fails or anti-bot block is detected, it automatically
// retries via ScraperAPI fallback if configured.
func Fetch(ctx context.Context, targetURL string, forceRender bool) (string, bool, error) {
	return FetchWithPool(ctx, nil, targetURL, forceRender)
}

// FetchWithKey performs HTML fetching with explicit ScraperAPI key for fallback routing.
func FetchWithKey(ctx context.Context, apiKey string, targetURL string, forceRender bool) (string, bool, error) {
	return FetchWithPoolKey(ctx, nil, apiKey, targetURL, forceRender)
}

// FetchWithPool fetches HTML using a provided Browser Pool for rendered requests.
func FetchWithPool(ctx context.Context, pool *browser.Pool, targetURL string, forceRender bool) (string, bool, error) {
	apiKey := os.Getenv("SCRAPERAPI_KEY")
	return FetchWithPoolKey(ctx, pool, apiKey, targetURL, forceRender)
}

// FetchWithPoolKey performs HTML fetching using a provided Browser Pool and explicit ScraperAPI key.
func FetchWithPoolKey(ctx context.Context, pool *browser.Pool, apiKey string, targetURL string, forceRender bool) (string, bool, error) {
	apiKey = strings.TrimSpace(apiKey)

	renderFunc := func(c context.Context, u string, timeout time.Duration) (string, error) {
		if pool != nil {
			return pool.FetchRendered(c, u, timeout)
		}
		return browser.FetchRendered(c, u, timeout)
	}

	// 1. Check if domain circuit breaker is already tripped
	if browser.DefaultCircuitBreaker.IsTripped(targetURL) {
		if apiKey != "" {
			slog.Info("Circuit breaker active for domain. Bypassing direct fetch, routing via ScraperAPI", "url", targetURL)
			fallbackHTML, err := browser.FetchViaFallback(ctx, apiKey, targetURL, forceRender)
			if err == nil {
				return fallbackHTML, forceRender, nil
			}
			slog.Warn("Circuit breaker fallback attempt failed. Attempting direct fetch", "url", targetURL, "error", err)
		} else {
			slog.Warn("Circuit breaker active for domain, but SCRAPERAPI_KEY is unset", "url", targetURL)
		}
	}

	// 2. Perform direct fetch (static colly or headless go-rod)
	var rawHTML string
	var usedBrowser bool
	var fetchErr error

	if forceRender {
		rawHTML, fetchErr = renderFunc(ctx, targetURL, 30*time.Second)
		usedBrowser = true
	} else {
		staticHTML, err := FetchStatic(ctx, targetURL)
		if err == nil {
			cleanText, cleanErr := CleanHTML(staticHTML)
			if cleanErr == nil && isContentSufficient(staticHTML, cleanText) {
				rawHTML = staticHTML
				usedBrowser = false
			}
		}

		if rawHTML == "" {
			renderedHTML, rErr := renderFunc(ctx, targetURL, 30*time.Second)
			if rErr == nil {
				rawHTML = renderedHTML
				usedBrowser = true
			} else {
				if fetchErr == nil {
					fetchErr = rErr
				}
				if staticHTML != "" {
					rawHTML = staticHTML
					usedBrowser = false
				}
			}
		}
	}

	// Check if response is blocked or failed
	isBlocked := false
	if rawHTML != "" && browser.IsBlocked(200, rawHTML) {
		isBlocked = true
	}

	if fetchErr != nil || isBlocked || rawHTML == "" {
		count := browser.DefaultCircuitBreaker.RecordFailure(targetURL)
		slog.Warn("Direct fetch failed or blocked", "url", targetURL, "failure_count", count, "error", fetchErr, "blocked", isBlocked)

		if apiKey != "" {
			slog.Info("Retrying fetch via ScraperAPI fallback", "url", targetURL, "render", forceRender)
			fallbackHTML, fbErr := browser.FetchViaFallback(ctx, apiKey, targetURL, forceRender)
			if fbErr == nil {
				slog.Info("ScraperAPI fallback successfully retrieved content", "url", targetURL)
				browser.DefaultCircuitBreaker.RecordSuccess(targetURL)
				return fallbackHTML, forceRender, nil
			}
			slog.Error("ScraperAPI fallback failed", "url", targetURL, "error", fbErr)
		} else {
			slog.Warn("SCRAPERAPI_KEY is not configured — degrading gracefully without fallback", "url", targetURL)
		}

		if fetchErr != nil {
			return "", usedBrowser, fetchErr
		}
		if isBlocked {
			return rawHTML, usedBrowser, fmt.Errorf("page content at %s appears blocked/challenged by anti-bot protection", targetURL)
		}
		return "", usedBrowser, fmt.Errorf("received empty content from %s", targetURL)
	}

	// Success on direct fetch
	browser.DefaultCircuitBreaker.RecordSuccess(targetURL)
	return rawHTML, usedBrowser, nil
}

// isContentSufficient checks if static HTML text is substantial enough or if it's an unrendered SPA container.
func isContentSufficient(rawHTML string, cleanText string) bool {
	clean := strings.TrimSpace(cleanText)
	if len(clean) == 0 {
		return false
	}
	lowerRaw := strings.ToLower(rawHTML)
	if (strings.Contains(lowerRaw, `<div id="root"></div>`) ||
		strings.Contains(lowerRaw, `<div id="app"></div>`) ||
		strings.Contains(lowerRaw, `<div id="__next"></div>`) ||
		strings.Contains(lowerRaw, "you need to enable javascript")) && len(clean) < 300 {
		return false
	}
	return true
}

