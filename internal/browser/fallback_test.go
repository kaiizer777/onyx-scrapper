package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestIsBlocked(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   bool
	}{
		{"200 OK Normal HTML", 200, "<html><body><h1>Welcome to My Site</h1></body></html>", false},
		{"403 Forbidden", 403, "Forbidden", true},
		{"429 Too Many Requests", 429, "Rate limit exceeded", true},
		{"503 Service Unavailable", 503, "Service Unavailable", true},
		{"406 Not Acceptable", 406, "Not Acceptable", true},
		{"Cloudflare Challenge 200 OK", 200, "<html><head><title>Just a moment...</title></head><body>Cloudflare browser verification</body></html>", true},
		{"Datadome Challenge 200 OK", 200, "<script src='https://static.datadome.co/tags.js'></script>", true},
		{"reCAPTCHA Challenge", 200, "<div class='g-recaptcha' data-sitekey='foo'></div>", true},
		{"Turnstile Challenge", 200, "<div class='cf-turnstile'></div>", true},
		{"Bot Detection Text", 200, "Please turn JavaScript on and reload the page for bot detection", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsBlocked(tt.statusCode, tt.body)
			if got != tt.expected {
				t.Errorf("IsBlocked(%d, %q) = %v; want %v", tt.statusCode, tt.body, got, tt.expected)
			}
		})
	}
}

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com/path/to/page?query=1", "example.com"},
		{"http://sub.domain.org:8080/foo", "sub.domain.org"},
		{"example.net", "example.net"},
		{"  HTTPS://MySite.COM/Page  ", "mysite.com"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ExtractDomain(tt.input)
			if got != tt.expected {
				t.Errorf("ExtractDomain(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(2, 200*time.Millisecond)

	urlStr := "https://test-domain.com/item/1"

	if cb.IsTripped(urlStr) {
		t.Errorf("Expected initial circuit breaker state to be false (untripped)")
	}

	// 1st failure
	count := cb.RecordFailure(urlStr)
	if count != 1 {
		t.Errorf("RecordFailure count = %d; want 1", count)
	}
	if cb.IsTripped(urlStr) {
		t.Errorf("Circuit breaker should not trip after 1 failure (threshold is 2)")
	}

	// 2nd failure -> should trip!
	count = cb.RecordFailure(urlStr)
	if count != 2 {
		t.Errorf("RecordFailure count = %d; want 2", count)
	}
	if !cb.IsTripped(urlStr) {
		t.Errorf("Circuit breaker should be tripped after 2 failures")
	}

	// Other domain should remain untripped
	otherURL := "https://other-domain.com/page"
	if cb.IsTripped(otherURL) {
		t.Errorf("Other domain should not be affected by test-domain.com failure")
	}

	// Record success -> should reset
	cb.RecordSuccess(urlStr)
	if cb.IsTripped(urlStr) {
		t.Errorf("Circuit breaker should reset after RecordSuccess")
	}
	if cb.GetFailureCount(urlStr) != 0 {
		t.Errorf("Failure count should be 0 after RecordSuccess")
	}

	// Test cooldown expiration
	cb.RecordFailure(urlStr)
	cb.RecordFailure(urlStr)
	if !cb.IsTripped(urlStr) {
		t.Errorf("Circuit breaker should be tripped after 2 failures")
	}

	time.Sleep(250 * time.Millisecond)
	if cb.IsTripped(urlStr) {
		t.Errorf("Circuit breaker should auto-reset after cooldown period expires")
	}
}

func TestFetchViaFallback(t *testing.T) {
	t.Run("Empty API Key", func(t *testing.T) {
		_, err := FetchViaFallback(context.Background(), "", "https://example.com", false)
		if err == nil {
			t.Errorf("Expected error for empty API key")
		}
	})

	t.Run("Successful ScraperAPI Call", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := r.URL.Query().Get("api_key")
			targetURL := r.URL.Query().Get("url")
			render := r.URL.Query().Get("render")

			if apiKey != "test-key-123" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if targetURL != "https://example.com/target" {
				http.Error(w, "Invalid target URL", http.StatusBadRequest)
				return
			}
			if render != "true" {
				http.Error(w, "Expected render=true", http.StatusBadRequest)
				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html><body>ScraperAPI content</body></html>"))
		}))
		defer ts.Close()

		// Override endpoint for testing by passing custom URL or testing response logic
		// We can construct request using ts.URL endpoint
		reqURL := fmt.Sprintf("%s?api_key=%s&url=%s&render=true",
			ts.URL,
			url.QueryEscape("test-key-123"),
			url.QueryEscape("https://example.com/target"),
		)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, reqURL, nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Status code = %d; want 200", resp.StatusCode)
		}
	})
}
