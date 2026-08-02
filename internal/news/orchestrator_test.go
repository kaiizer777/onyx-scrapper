package news

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/profile"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestDeduplicateNewsItems(t *testing.T) {
	o := &Orchestrator{}

	items := []store.NewsItem{
		{
			Title: "OpenAI Releases New Model - TechCrunch",
			URL:   "https://techcrunch.com/2026/08/01/openai-new-model?utm_source=twitter&utm_medium=social",
		},
		{
			Title: "OpenAI Releases New Model - TechCrunch",
			URL:   "https://techcrunch.com/2026/08/01/openai-new-model",
		},
		{
			Title: "Google Gemini 3.5 Released - Reuters",
			URL:   "https://reuters.com/tech/gemini-35",
		},
	}

	deduped := o.deduplicateNewsItems(items)

	if len(deduped) != 2 {
		t.Fatalf("expected 2 deduped items, got %d", len(deduped))
	}

	if deduped[0].URL != items[0].URL {
		t.Errorf("expected first item URL to be preserved, got %s", deduped[0].URL)
	}
	if deduped[1].Title != items[2].Title {
		t.Errorf("expected second unique item to be Gemini 3.5, got %s", deduped[1].Title)
	}
}

func TestOrchestratorRunEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_news.db")

	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()


	profMgr := profile.NewManager(st, profile.Config{MaxFields: 10})
	p, err := profMgr.GetOrCreateDefaultProfile()
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	_, err = profMgr.AddField(p.ID, "AI/ML", "LLM, generative AI", 1, true)
	if err != nil {
		t.Fatalf("failed to add field: %v", err)
	}
	_, err = profMgr.AddField(p.ID, "Gaming", "unreal engine, playstation", 2, true)
	if err != nil {
		t.Fatalf("failed to add field 2: %v", err)
	}

	mockXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Google News</title>
    <link>https://news.google.com</link>
    <item>
      <title>Latest AI Breakthrough - Tech</title>
      <link>https://example.com/ai-breakthrough?utm_source=rss</link>
      <pubDate>Sun, 02 Aug 2026 01:00:00 GMT</pubDate>
      <description>AI technology makes leaps forward in reasoning and code generation.</description>
      <source url="https://example.com">Tech</source>
    </item>
  </channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockXML))
	}))
	defer server.Close()

	cfg := &config.Config{
		News: &config.NewsConfig{
			DefaultWindow:       "24h",
			ArticlesPerField:    5,
			MinArticlesBackfill: 1,
		},
	}

	orch := NewOrchestrator(st, profMgr, nil, nil, cfg)
	orch.httpClient = &http.Client{
		Transport: &mockRedirectTransport{targetURL: server.URL},
		Timeout:   5 * time.Second,
	}

	win := ParseRecencyWindow("last 24 hours", "24h")
	run, fieldNews, err := orch.Run(context.Background(), win, p.ID)
	if err != nil {
		t.Fatalf("Orchestrator.Run failed: %v", err)
	}

	if run == nil {
		t.Fatal("expected non-nil news run")
	}
	if run.Status != "completed" {
		t.Errorf("expected run status 'completed', got %q", run.Status)
	}

	if len(fieldNews) != 2 {
		t.Fatalf("expected news for 2 fields, got %d", len(fieldNews))
	}

	storedItems, err := st.GetNewsItemsForRun(run.ID)
	if err != nil {
		t.Fatalf("failed to fetch stored news items: %v", err)
	}

	if len(storedItems) == 0 {
		t.Errorf("expected stored news items, got 0")
	}
}
