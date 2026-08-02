package news

import (
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestFilterItemsByRecency_DropsOldKeepsRecent(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)

	items := []store.NewsItem{
		{URL: "https://a.example/old", Title: "old", PublishedAt: ptrTime(now.Add(-48 * time.Hour))},
		{URL: "https://a.example/recent", Title: "recent", PublishedAt: ptrTime(now.Add(-2 * time.Hour))},
		{URL: "https://a.example/exact", Title: "exact", PublishedAt: ptrTime(since)},
		{URL: "https://a.example/nodate", Title: "nodate", PublishedAt: nil},
	}

	got := filterItemsByRecency(items, since, "test-field")

	if len(got) != 3 {
		t.Fatalf("expected 3 items (recent + exact-boundary + nodate), got %d", len(got))
	}

	want := []string{
		"https://a.example/recent",
		"https://a.example/exact",
		"https://a.example/nodate",
	}
	for i, w := range want {
		if got[i].URL != w {
			t.Errorf("got[%d].URL=%q, want %q", i, got[i].URL, w)
		}
	}
}

func TestFilterItemsByRecency_ZeroSince_NoOp(t *testing.T) {
	// A zero since means "no recency constraint" — the function
	// must short-circuit and return the original slice so callers
	// (orchestrator or view-build) don't have to branch.
	items := []store.NewsItem{
		{URL: "https://a.example/x", PublishedAt: ptrTime(time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC))},
		{URL: "https://a.example/y"},
	}

	got := filterItemsByRecency(items, time.Time{}, "test-field")
	if len(got) != 2 {
		t.Fatalf("zero since should be a no-op; got %d items, want 2", len(got))
	}
}

func TestFilterItemsByRecency_AllOldReturnsEmpty(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)

	items := []store.NewsItem{
		{URL: "https://a.example/a", PublishedAt: ptrTime(now.Add(-72 * time.Hour))},
		{URL: "https://a.example/b", PublishedAt: ptrTime(now.Add(-100 * time.Hour))},
	}

	got := filterItemsByRecency(items, since, "test-field")
	if len(got) != 0 {
		t.Fatalf("expected 0 items (all older than since), got %d", len(got))
	}
}

func TestFilterItemsByRecency_AllNilKept(t *testing.T) {
	// The user explicitly chose "keep if unknown". A field whose
	// RSS feed never ships dates should not be wiped out.
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	since := now.Add(-24 * time.Hour)

	items := []store.NewsItem{
		{URL: "https://a.example/a"},
		{URL: "https://a.example/b"},
	}

	got := filterItemsByRecency(items, since, "test-field")
	if len(got) != 2 {
		t.Fatalf("expected 2 nil-date items kept, got %d", len(got))
	}
}
