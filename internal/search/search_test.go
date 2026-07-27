package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestSearchSearXNGSuccess(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "golang" {
			t.Errorf("expected query 'golang', got %q", r.URL.Query().Get("q"))
		}
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("expected format 'json', got %q", r.URL.Query().Get("format"))
		}

		resp := searxngResponse{
			Query: "golang",
			Results: []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Content string `json:"content"`
				Snippet string `json:"snippet"`
			}{
				{
					Title:   "Go Programming Language",
					URL:     "https://go.dev",
					Snippet: "Build fast, reliable software at scale.",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	client := NewSearXNGClient(mockServer.URL, nil)
	clientResults, clientErr := client.Search(context.Background(), "golang")
	if clientErr != nil {
		t.Fatalf("SearXNGClient search failed: %v", clientErr)
	}
	if len(clientResults) != 1 || clientResults[0].URL != "https://go.dev" {
		t.Errorf("unexpected SearXNGClient results: %+v", clientResults)
	}

	svc := NewService(nil, WithSearXNGURL(mockServer.URL))
	ctx := context.Background()

	res, err := svc.Search(ctx, "golang")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if res.Query != "golang" {
		t.Errorf("expected query 'golang', got %q", res.Query)
	}
	if len(res.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res.Results))
	}
	if res.Results[0].Title != "Go Programming Language" {
		t.Errorf("expected title 'Go Programming Language', got %q", res.Results[0].Title)
	}
	if res.Results[0].URL != "https://go.dev" {
		t.Errorf("expected URL 'https://go.dev', got %q", res.Results[0].URL)
	}
}

func TestSearchFallbackToStore(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_search.db")

	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()
	defer os.Remove(dbPath)

	_, err = st.SavePage("https://example.com/go-article", "<html><body>Go scraping is fast and efficient.</body></html>", "Go scraping is fast and efficient.")
	if err != nil {
		t.Fatalf("failed to save page: %v", err)
	}

	// Point SearXNG to an invalid port to trigger fallback
	svc := NewService(st, WithSearXNGURL("http://127.0.0.1:54321"))
	ctx := context.Background()

	res, err := svc.Search(ctx, "scraping")
	if err != nil {
		t.Fatalf("Search failed during fallback: %v", err)
	}

	if res.Query != "scraping" {
		t.Errorf("expected query 'scraping', got %q", res.Query)
	}
	if len(res.Results) != 1 {
		t.Fatalf("expected 1 fallback result from store, got %d", len(res.Results))
	}
	if res.Results[0].URL != "https://example.com/go-article" {
		t.Errorf("expected URL 'https://example.com/go-article', got %q", res.Results[0].URL)
	}
}
