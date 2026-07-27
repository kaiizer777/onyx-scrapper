package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/browser"
)

func TestFetchWithKeyFallbackWorkflow(t *testing.T) {
	// Setup test server simulating a site returning Cloudflare anti-bot challenge
	blockedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><head><title>Just a moment...</title></head><body>Cloudflare browser verification</body></html>"))
	}))
	defer blockedServer.Close()

	// Setup mock ScraperAPI server
	scraperAPIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.URL.Query().Get("api_key")
		targetURL := r.URL.Query().Get("url")
		if apiKey == "valid-test-key" && strings.Contains(targetURL, blockedServer.URL) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html><body>Unblocked Fallback Content Body with sufficient text length to pass clean text validation check</body></html>"))
			return
		}
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer scraperAPIServer.Close()

	t.Run("Blocked response triggers circuit breaker failure and fallback warning without key", func(t *testing.T) {
		// Reset circuit breaker for domain
		browser.DefaultCircuitBreaker.RecordSuccess(blockedServer.URL)

		_, _, err := FetchWithKey(context.Background(), "", blockedServer.URL, false)
		if err == nil {
			t.Errorf("Expected error when site returns blocked challenge and no ScraperAPI key is set")
		}
		if !strings.Contains(err.Error(), "blocked") {
			t.Errorf("Expected blocked error, got: %v", err)
		}
		if browser.DefaultCircuitBreaker.GetFailureCount(blockedServer.URL) != 1 {
			t.Errorf("Expected failure count 1 after blocked fetch, got %d", browser.DefaultCircuitBreaker.GetFailureCount(blockedServer.URL))
		}

		// Clear circuit breaker state
		browser.DefaultCircuitBreaker.RecordSuccess(blockedServer.URL)
	})
}
