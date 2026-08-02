package news

import (
	"strings"
	"testing"
	"time"
)

// TestBuildDigestView_NilSafe ensures BuildDigestView handles the nil
// digest case without panicking — every formatter downstream assumes
// BuildDigestView(nil) == nil and bails out cleanly.
func TestBuildDigestView_NilSafe(t *testing.T) {
	if v := BuildDigestView(nil); v != nil {
		t.Errorf("expected nil view for nil digest, got %+v", v)
	}
}

// TestBuildDigestView_StampsWindowOnEveryField is the Phase 9
// contract: the resolved window phrase is stamped onto every field
// view so each per-field section header can render it without
// reaching back into the parent digest.
func TestBuildDigestView_StampsWindowOnEveryField(t *testing.T) {
	d := &NewsDigest{
		RunID:  11,
		Window: "past week",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "AI", Items: []DigestItem{{Headline: "x"}}},
			{FieldID: 2, FieldName: "Sports", Items: []DigestItem{}},
		},
	}
	v := BuildDigestView(d)
	if v == nil {
		t.Fatal("view should not be nil")
	}
	if v.Window != "past week" {
		t.Errorf("expected view.Window='past week', got %q", v.Window)
	}
	if len(v.Fields) != 2 {
		t.Fatalf("expected 2 field views, got %d", len(v.Fields))
	}
	for _, fv := range v.Fields {
		if fv.Window != "past week" {
			t.Errorf("field %q missing stamped window, got %q", fv.FieldName, fv.Window)
		}
	}
}

// TestBuildDigestView_PrecomputesRelativeTime ensures the relative
// time string is computed at view-build time so the three renderers
// see a consistent value (and don't race on time.Now() between
// per-field headers).
func TestBuildDigestView_PrecomputesRelativeTime(t *testing.T) {
	now := time.Now()
	d := &NewsDigest{
		Window: "24h",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "AI", Items: []DigestItem{
				{Headline: "x", PublishedAt: ptrTime(now.Add(-2 * time.Hour))},
				{Headline: "y"}, // no published time
			}},
		},
	}
	v := BuildDigestView(d)
	if len(v.Fields) != 1 || len(v.Fields[0].Items) != 2 {
		t.Fatalf("unexpected view shape: %+v", v)
	}
	first := v.Fields[0].Items[0]
	if first.RelativeTime == "" {
		t.Error("expected non-empty relative time for item with PublishedAt")
	}
	if !strings.Contains(first.RelativeTime, "hour") {
		t.Errorf("expected 'hour' in relative time, got %q", first.RelativeTime)
	}
	second := v.Fields[0].Items[1]
	if second.RelativeTime != "" {
		t.Errorf("expected empty relative time for item without PublishedAt, got %q", second.RelativeTime)
	}
}

// TestBuildDigestView_ItemCountMatchesItems ensures ItemCount is
// precomputed and equal to len(Items) so renderers don't have to
// re-derive it (and don't get it wrong on the empty-field branch).
func TestBuildDigestView_ItemCountMatchesItems(t *testing.T) {
	d := &NewsDigest{
		Window: "24h",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "AI", Items: []DigestItem{
				{Headline: "a"}, {Headline: "b"}, {Headline: "c"},
			}},
			{FieldID: 2, FieldName: "Sports", Items: []DigestItem{}},
		},
	}
	v := BuildDigestView(d)
	if v.Fields[0].ItemCount != 3 {
		t.Errorf("expected ItemCount=3, got %d", v.Fields[0].ItemCount)
	}
	if v.Fields[1].ItemCount != 0 {
		t.Errorf("expected ItemCount=0 for empty field, got %d", v.Fields[1].ItemCount)
	}
	if len(v.Fields[0].Items) != v.Fields[0].ItemCount {
		t.Errorf("ItemCount (%d) should match len(Items) (%d)",
			v.Fields[0].ItemCount, len(v.Fields[0].Items))
	}
}

// TestBuildDigestView_HeadlineFallback uses the URL as a fallback
// when the headline is empty — preserves the existing Web UI behavior
// where `item.title || item.url` is shown.
func TestBuildDigestView_HeadlineFallback(t *testing.T) {
	d := &NewsDigest{
		Window: "24h",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "AI", Items: []DigestItem{
				{URL: "https://example.com/1", Headline: ""},
				{URL: "https://example.com/2", Headline: "   "}, // whitespace only
			}},
		},
	}
	v := BuildDigestView(d)
	if v.Fields[0].Items[0].Headline != "https://example.com/1" {
		t.Errorf("expected URL fallback for empty headline, got %q", v.Fields[0].Items[0].Headline)
	}
	if v.Fields[0].Items[1].Headline != "https://example.com/2" {
		t.Errorf("expected URL fallback for whitespace headline, got %q", v.Fields[0].Items[1].Headline)
	}
}

// TestBuildDigestView_StripsWhitespace ensures leading/trailing
// whitespace is trimmed off user-supplied strings so renderers don't
// produce awkward spacing in headers or items.
func TestBuildDigestView_StripsWhitespace(t *testing.T) {
	d := &NewsDigest{
		Window: "  past 24h  ",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "  AI  ", Items: []DigestItem{
				{Headline: "  H  ", Source: "  S  ", URL: "  https://u  "},
			}},
		},
	}
	v := BuildDigestView(d)
	if v.Window != "past 24h" {
		t.Errorf("expected trimmed window, got %q", v.Window)
	}
	if v.Fields[0].FieldName != "AI" {
		t.Errorf("expected trimmed field name, got %q", v.Fields[0].FieldName)
	}
	it := v.Fields[0].Items[0]
	if it.Headline != "H" || it.Source != "S" || it.URL != "https://u" {
		t.Errorf("expected trimmed item fields, got %+v", it)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
