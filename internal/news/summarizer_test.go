package news

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestSummarizerFallbackTakeaway(t *testing.T) {
	s := NewSummarizer(nil, nil, nil, quality.NewBudget(10), nil)

	// Multi-sentence snippet
	summary := "OpenAI released GPT-5 today with improved reasoning capabilities. The model sets new benchmarks across coding and math tasks. Further updates are expected next week."
	takeaway := s.fallbackTakeaway(summary)

	expected := "OpenAI released GPT-5 today with improved reasoning capabilities. The model sets new benchmarks across coding and math tasks."
	if takeaway != expected {
		t.Errorf("expected takeaway %q, got %q", expected, takeaway)
	}

	// Empty summary
	emptyTakeaway := s.fallbackTakeaway("")
	if emptyTakeaway != "No summary snippet available for this news item." {
		t.Errorf("expected default empty takeaway, got %q", emptyTakeaway)
	}
}

func TestSummarizerConfidenceFlags(t *testing.T) {
	authMgr := quality.NewAuthorityManager()
	_ = authMgr.LoadTiers("../../config/authority_tiers.yaml")
	s := NewSummarizer(nil, nil, authMgr, quality.NewBudget(10), nil)

	items := []store.NewsItem{
		{
			Title:  "Item 1",
			URL:    "https://reuters.com/tech/ai-news",
			Source: "Reuters",
		},
		{
			Title:  "Item 2",
			URL:    "https://unknowntechblog.org/ai-news",
			Source: "Unknown Tech Blog",
		},
	}

	flags := s.computeConfidenceFlags(items)
	if len(flags) != 2 {
		t.Fatalf("expected 2 confidence flags, got %d", len(flags))
	}

	// Multi-domain scenario (reuters.com + unknowntechblog.org)
	// reuters.com is recognized as TierPrimary by AuthorityManager
	if flags[0] != "(corroborated — primary source)" {
		t.Errorf("expected primary source corroborated flag for Reuters, got %q", flags[0])
	}
	if flags[1] != "(corroborated: 2 sources)" {
		t.Errorf("expected multi-source corroborated flag for blog, got %q", flags[1])
	}
}

func TestSummarizerFieldPriorityOrder(t *testing.T) {
	s := NewSummarizer(nil, nil, nil, quality.NewBudget(10), nil)

	run := &store.NewsRun{
		ID:        1,
		ProfileID: 1,
		Window:    "24h",
	}

	fetched := []FetchedFieldNews{
		{
			Field: store.ProfileField{
				ID:            20,
				FieldName:     "Gaming",
				PriorityOrder: 2,
			},
			Items: []store.NewsItem{
				{Title: "Game News 1", URL: "https://example.com/game1", Summary: "Gaming snippet."},
			},
		},
		{
			Field: store.ProfileField{
				ID:            10,
				FieldName:     "AI/ML",
				PriorityOrder: 1,
			},
			Items: []store.NewsItem{
				{Title: "AI News 1", URL: "https://example.com/ai1", Summary: "AI snippet."},
			},
		},
	}

	digest, err := s.CompileDigest(context.Background(), run, fetched)
	if err != nil {
		t.Fatalf("CompileDigest failed: %v", err)
	}

	if digest == nil {
		t.Fatal("expected non-nil digest")
	}

	if len(digest.Fields) != 2 {
		t.Fatalf("expected 2 field digests, got %d", len(digest.Fields))
	}

	// AI/ML has PriorityOrder=1, Gaming has PriorityOrder=2 -> AI/ML must be first
	if digest.Fields[0].FieldName != "AI/ML" {
		t.Errorf("expected first field to be AI/ML (PriorityOrder=1), got %q", digest.Fields[0].FieldName)
	}
	if digest.Fields[1].FieldName != "Gaming" {
		t.Errorf("expected second field to be Gaming (PriorityOrder=2), got %q", digest.Fields[1].FieldName)
	}

	if len(digest.Fields[0].Items) != 1 {
		t.Fatalf("expected 1 item in AI/ML field digest, got %d", len(digest.Fields[0].Items))
	}
	if digest.Fields[0].Items[0].Headline != "AI News 1" {
		t.Errorf("expected headline 'AI News 1', got %q", digest.Fields[0].Items[0].Headline)
	}
}

func TestSummarizerRerankingFallback(t *testing.T) {
	reranker := discovery.NewJinaReranker("", false) // disabled
	s := NewSummarizer(nil, reranker, nil, quality.NewBudget(10), nil)

	field := store.ProfileField{
		ID:          1,
		FieldName:   "AI",
		KeywordsCSV: "LLM, AI",
	}

	items := []store.NewsItem{
		{Title: "Article 1", URL: "https://a.com/1", Summary: "Snippet 1"},
		{Title: "Article 2", URL: "https://b.com/2", Summary: "Snippet 2"},
	}

	fd, err := s.SummarizeField(context.Background(), field, items)
	if err != nil {
		t.Fatalf("SummarizeField failed: %v", err)
	}

	if len(fd.Items) != 2 {
		t.Fatalf("expected 2 items in field digest, got %d", len(fd.Items))
	}
	// Verify disabled reranker preserves original order
	if fd.Items[0].Headline != "Article 1" || fd.Items[1].Headline != "Article 2" {
		t.Errorf("expected original item order preserved when reranker disabled")
	}
}

func TestOrchestratorSummarizeDigestIntegration(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_summarizer.db")

	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	p, err := st.CreateProfile("Default Profile")
	if err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}

	run, err := st.CreateNewsRun(p.ID, "24h")
	if err != nil {
		t.Fatalf("failed to create news run: %v", err)
	}

	orch := &Orchestrator{
		store:  st,
		budget: quality.NewBudget(10),
	}

	fetched := []FetchedFieldNews{
		{
			Field: store.ProfileField{
				ID:            1,
				FieldName:     "Tech",
				PriorityOrder: 1,
			},
			Items: []store.NewsItem{
				{
					RunID:       run.ID,
					FieldID:     1,
					Title:       "Tech Headline",
					URL:         "https://techcrunch.com/item1",
					Source:      "TechCrunch",
					Summary:     "First sentence of summary. Second sentence of summary.",
					PublishedAt: func() *time.Time { now := time.Now(); return &now }(),
				},
			},
		},
	}

	digest, err := orch.SummarizeDigest(context.Background(), run, fetched)
	if err != nil {
		t.Fatalf("SummarizeDigest failed: %v", err)
	}

	if digest.RunID != run.ID {
		t.Errorf("expected run ID %d, got %d", run.ID, digest.RunID)
	}
	if len(digest.Fields) != 1 {
		t.Fatalf("expected 1 field digest, got %d", len(digest.Fields))
	}
	if digest.Fields[0].Items[0].Takeaway == "" {
		t.Errorf("expected non-empty takeaway in digest item")
	}
}
