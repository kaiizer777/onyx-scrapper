package browser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// BlockedKeywords contains common anti-bot/CAPTCHA indicators present in blocked HTML responses.
var BlockedKeywords = []string{
	"cf-browser-verification",
	"cf-wrapper",
	"just a moment...",
	"attention required! | cloudflare",
	"px-captcha",
	"g-recaptcha",
	"hcaptcha",
	"cf-turnstile",
	"turnstile",
	"access denied",
	"please enable cookies",
	"please turn javascript on and reload",
	"bot detection",
	"checking your browser before accessing",
	"ddos protection by",
	"checking if the site connection is secure",
}

// IsBlocked checks if an HTTP status code or page HTML body contains anti-bot block signatures.
func IsBlocked(statusCode int, body string) bool {
	// Status code checks
	if statusCode == http.StatusForbidden ||
		statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusNotAcceptable {
		return true
	}

	if body == "" {
		return false
	}

	lowerBody := strings.ToLower(body)
	matchedKeyword := false
	for _, kw := range BlockedKeywords {
		if strings.Contains(lowerBody, kw) {
			matchedKeyword = true
			break
		}
	}

	if matchedKeyword {
		// Only flag as blocked if the page is suspiciously short (e.g., < 2000 chars)
		// Real challenge pages are minimal compared to full rendered content.
		if len(body) < 2000 {
			return true
		}
	}

	return false
}

// CircuitBreaker tracks failure counts per domain and trips into fallback mode when threshold is reached.
type CircuitBreaker struct {
	mu          sync.RWMutex
	failures    map[string]int
	lastFailure map[string]time.Time
	threshold   int
	cooldown    time.Duration
}

// NewCircuitBreaker initializes a new CircuitBreaker instance with threshold and cooldown.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 2
	}
	if cooldown <= 0 {
		cooldown = 10 * time.Minute
	}
	return &CircuitBreaker{
		failures:    make(map[string]int),
		lastFailure: make(map[string]time.Time),
		threshold:   threshold,
		cooldown:    cooldown,
	}
}

// DefaultCircuitBreaker is the package-wide circuit breaker instance (threshold 2, cooldown 10 min).
var DefaultCircuitBreaker = NewCircuitBreaker(2, 10*time.Minute)

// ExtractDomain extracts the hostname from a URL or raw domain string.
func ExtractDomain(raw string) string {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return strings.ToLower(raw)
	}
	return strings.ToLower(u.Hostname())
}

// RecordFailure increments the failure count for a domain and returns the updated count.
func (cb *CircuitBreaker) RecordFailure(rawURLOrDomain string) int {
	domain := ExtractDomain(rawURLOrDomain)
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Reset if cooldown expired
	if last, exists := cb.lastFailure[domain]; exists && time.Since(last) > cb.cooldown {
		cb.failures[domain] = 0
	}

	cb.failures[domain]++
	cb.lastFailure[domain] = time.Now()
	return cb.failures[domain]
}

// RecordSuccess clears failure records for a domain.
func (cb *CircuitBreaker) RecordSuccess(rawURLOrDomain string) {
	domain := ExtractDomain(rawURLOrDomain)
	cb.mu.Lock()
	defer cb.mu.Unlock()

	delete(cb.failures, domain)
	delete(cb.lastFailure, domain)
}

// IsTripped returns true if the domain has exceeded the failure threshold within cooldown.
func (cb *CircuitBreaker) IsTripped(rawURLOrDomain string) bool {
	domain := ExtractDomain(rawURLOrDomain)
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	last, exists := cb.lastFailure[domain]
	if !exists {
		return false
	}

	if time.Since(last) > cb.cooldown {
		return false
	}

	return cb.failures[domain] >= cb.threshold
}

// GetFailureCount returns current recorded failures for a domain.
func (cb *CircuitBreaker) GetFailureCount(rawURLOrDomain string) int {
	domain := ExtractDomain(rawURLOrDomain)
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	last, exists := cb.lastFailure[domain]
	if !exists || time.Since(last) > cb.cooldown {
		return 0
	}
	return cb.failures[domain]
}

// FetchViaFallback fetches targetURL through the ScraperAPI free-tier service.
func FetchViaFallback(ctx context.Context, apiKey string, targetURL string, render bool) (string, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", fmt.Errorf("ScraperAPI key is empty: fallback service unavailable")
	}

	reqURL := fmt.Sprintf("http://api.scraperapi.com?api_key=%s&url=%s",
		url.QueryEscape(apiKey),
		url.QueryEscape(targetURL),
	)
	if render {
		reqURL += "&render=true"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create ScraperAPI request: %w", err)
	}

	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ScraperAPI request error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read ScraperAPI response body: %w", err)
	}

	bodyStr := string(bodyBytes)

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ScraperAPI error (status %d): %s", resp.StatusCode, bodyStr)
	}

	if bodyStr == "" {
		return "", fmt.Errorf("ScraperAPI returned empty body for %s", targetURL)
	}

	if IsBlocked(resp.StatusCode, bodyStr) {
		return "", fmt.Errorf("ScraperAPI response still appears blocked/challenged for %s", targetURL)
	}

	return bodyStr, nil
}
