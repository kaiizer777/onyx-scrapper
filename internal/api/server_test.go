package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/search"
)

func TestServerPing(t *testing.T) {
	srv := NewServer(WithPort(9090))
	ts := httptest.NewServer(srv.httpSrv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/ping")
	if err != nil {
		t.Fatalf("Failed to GET /ping: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if data["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", data["status"])
	}
}

func TestServerSearchGETAndPOST(t *testing.T) {
	// Create mock SearXNG server
	mockSearXNG := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"query": r.URL.Query().Get("q"),
			"results": []map[string]string{
				{
					"title":   "Test Result",
					"url":     "https://example.com/test",
					"snippet": "Test snippet text",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockSearXNG.Close()

	searchSvc := search.NewService(nil, search.WithSearXNGURL(mockSearXNG.URL))
	srv := NewServer(WithSearchService(searchSvc))
	ts := httptest.NewServer(srv.httpSrv.Handler)
	defer ts.Close()

	// 1. GET /search?q=golang
	respGET, err := http.Get(ts.URL + "/search?q=golang")
	if err != nil {
		t.Fatalf("GET /search failed: %v", err)
	}
	defer respGET.Body.Close()

	var searchResGET search.SearchResponse
	if err := json.NewDecoder(respGET.Body).Decode(&searchResGET); err != nil {
		t.Fatalf("Failed to decode GET /search JSON: %v", err)
	}

	if searchResGET.Query != "golang" {
		t.Errorf("expected query 'golang', got %q", searchResGET.Query)
	}
	if len(searchResGET.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(searchResGET.Results))
	}
	if searchResGET.Results[0].URL != "https://example.com/test" {
		t.Errorf("expected result URL 'https://example.com/test', got %q", searchResGET.Results[0].URL)
	}

	// 2. POST /search {"q": "onyx"}
	postBody, _ := json.Marshal(map[string]string{"q": "onyx"})
	respPOST, err := http.Post(ts.URL+"/search", "application/json", bytes.NewBuffer(postBody))
	if err != nil {
		t.Fatalf("POST /search failed: %v", err)
	}
	defer respPOST.Body.Close()

	var searchResPOST search.SearchResponse
	if err := json.NewDecoder(respPOST.Body).Decode(&searchResPOST); err != nil {
		t.Fatalf("Failed to decode POST /search JSON: %v", err)
	}

	if searchResPOST.Query != "onyx" {
		t.Errorf("expected query 'onyx', got %q", searchResPOST.Query)
	}
}

func TestServerFetch(t *testing.T) {
	// Create mock target website
	targetSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>Hello Onyx API</h1><p>Test paragraph content</p></body></html>"))
	}))
	defer targetSite.Close()

	srv := NewServer()
	ts := httptest.NewServer(srv.httpSrv.Handler)
	defer ts.Close()

	reqBody, _ := json.Marshal(map[string]interface{}{
		"url":    targetSite.URL,
		"render": false,
	})

	resp, err := http.Post(ts.URL+"/fetch", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		t.Fatalf("POST /fetch failed: %v", err)
	}
	defer resp.Body.Close()

	var fetchRes fetchResponse
	if err := json.NewDecoder(resp.Body).Decode(&fetchRes); err != nil {
		t.Fatalf("Failed to decode POST /fetch JSON: %v", err)
	}

	if fetchRes.URL != targetSite.URL {
		t.Errorf("expected URL %q, got %q", targetSite.URL, fetchRes.URL)
	}
	if !bytes.Contains([]byte(fetchRes.CleanText), []byte("Hello Onyx API")) {
		t.Errorf("expected clean text to contain 'Hello Onyx API', got: %s", fetchRes.CleanText)
	}
}
