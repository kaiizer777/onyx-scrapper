package stealth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetRandomProfile(t *testing.T) {
	p1 := GetRandomProfile()
	p2 := GetRandomProfile()

	if p1.UserAgent == "" || p1.ViewportWidth <= 0 || p1.ViewportHeight <= 0 {
		t.Fatalf("Invalid profile p1: %+v", p1)
	}
	if p2.UserAgent == "" || p2.ViewportWidth <= 0 || p2.ViewportHeight <= 0 {
		t.Fatalf("Invalid profile p2: %+v", p2)
	}
}

func TestHumanDelayCtx(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := HumanDelayCtx(ctx, 500, 1000)
	duration := time.Since(start)

	if err == nil {
		t.Errorf("Expected context timeout error, got nil")
	}
	if duration >= 500*time.Millisecond {
		t.Errorf("Delay should have been cancelled early by context timeout, elapsed: %v", duration)
	}
}

func TestDomainRateLimiter(t *testing.T) {
	limiter := NewDomainRateLimiter(100*time.Millisecond, 1)

	ctx := context.Background()
	url := "https://example.com/test"

	start := time.Now()
	_ = limiter.Wait(ctx, url) // 1st call: instant
	_ = limiter.Wait(ctx, url) // 2nd call: delayed ~100ms
	elapsed := time.Since(start)

	if elapsed < 80*time.Millisecond {
		t.Errorf("Rate limiter did not throttle requests properly, elapsed: %v", elapsed)
	}
}

func TestRobotsChecker(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\nAllow: /public/\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	checker := NewRobotsChecker()
	ctx := context.Background()

	// Allowed path
	allowed, err := checker.IsAllowed(ctx, "OnyxBot", ts.URL+"/public/page")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !allowed {
		t.Errorf("Expected /public/page to be allowed")
	}

	// Disallowed path
	disallowed, err := checker.IsAllowed(ctx, "OnyxBot", ts.URL+"/private/secret")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if disallowed {
		t.Errorf("Expected /private/secret to be disallowed")
	}
}
