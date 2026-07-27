package browser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPoolInitializationAndLimits(t *testing.T) {
	pool := NewPool(3)
	if pool.MaxWorkers() != 3 {
		t.Fatalf("expected MaxWorkers=3, got %d", pool.MaxWorkers())
	}

	defer pool.Close()

	// Default fallback when <= 0
	poolDefault := NewPool(0)
	if poolDefault.MaxWorkers() < 2 {
		t.Fatalf("expected default MaxWorkers >= 2, got %d", poolDefault.MaxWorkers())
	}
	defer poolDefault.Close()
}

func TestPoolFetchRendered(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><h1 id="title">Pool Test Page</h1></body></html>`))
	}))
	defer ts.Close()

	pool := NewPool(2)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	html, err := pool.FetchRendered(ctx, ts.URL, 15*time.Second)
	if err != nil {
		t.Fatalf("FetchRendered failed: %v", err)
	}

	if html == "" {
		t.Fatalf("expected non-empty HTML")
	}
}
