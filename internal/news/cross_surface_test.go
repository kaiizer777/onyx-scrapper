package news

import (
	"strings"
	"testing"
	"time"
)

// TestCrossSurface_FieldSeparation is the cross-surface invariant
// guarantee for Phase 9: a single NewsDigest rendered through CLI,
// Telegram, and HTML must all preserve the same field boundaries —
// no item is ever merged into another field's section, every field
// appears with its own header in every surface, and the per-field
// window phrase is identical across surfaces.
func TestCrossSurface_FieldSeparation(t *testing.T) {
	d := &NewsDigest{
		RunID:  123,
		Window: "past 3 days",
		Fields: []FieldDigest{
			{
				FieldID:       1,
				FieldName:     "AI/ML",
				PriorityOrder: 1,
				Items: []DigestItem{
					{Headline: "AI headline A", URL: "https://a.example/1"},
					{Headline: "AI headline B", URL: "https://a.example/2"},
				},
			},
			{
				FieldID:       2,
				FieldName:     "Cricket",
				PriorityOrder: 2,
				Items: []DigestItem{
					{Headline: "Cricket headline A", URL: "https://c.example/1"},
				},
			},
			{
				FieldID:       3,
				FieldName:     "Gaming",
				PriorityOrder: 3,
				Items:         []DigestItem{},
			},
		},
	}

	view := BuildDigestView(d)
	if view == nil {
		t.Fatal("BuildDigestView returned nil")
	}

	cli := FormatCLIView(view, false)
	tgHeader := FormatTelegramHeaderView(view)
	tgFields := []string{}
	for _, fv := range view.Fields {
		tgFields = append(tgFields, FormatTelegramFieldView(fv))
	}
	tg := tgHeader + "\n" + strings.Join(tgFields, "\n")
	html := FormatHTMLView(view)

	surfaces := map[string]string{
		"CLI":      cli,
		"Telegram": tg,
		"HTML":     html,
	}

	// 1) Every surface must mention the resolved window and every
	// field name (case-insensitive — CLI/Telegram uppercase the
	// header line, HTML preserves the original case).
	up := func(s string) string { return strings.ToUpper(s) }
	containsCI := func(haystack, needle string) bool {
		return strings.Contains(strings.ToUpper(haystack), up(needle))
	}
	for name, out := range surfaces {
		if !strings.Contains(out, "past 3 days") {
			t.Errorf("%s: missing resolved window 'past 3 days'\n--- output ---\n%s", name, out)
		}
		for _, f := range []string{"AI/ML", "Cricket", "Gaming"} {
			if !containsCI(out, f) {
				t.Errorf("%s: missing field name %q (case-insensitive)\n--- output ---\n%s", name, f, out)
			}
		}
	}

	// 2) Every surface must show item counts consistent with the data:
	// 2 + 1 + 0 = 3 total items, 2 fields with items, 1 empty field.
	checks := []struct {
		surface, snippet string
	}{
		{"CLI", "FIELD: AI/ML · window: past 3 days · 2 items"},
		{"CLI", "FIELD: CRICKET · window: past 3 days · 1 item"},
		{"CLI", "FIELD: GAMING · window: past 3 days · 0 items"},
		{"Telegram", "━━ AI/ML · window: past 3 days · 2 items ━━"},
		{"Telegram", "━━ CRICKET · window: past 3 days · 1 item ━━"},
		{"Telegram", "━━ GAMING · window: past 3 days · 0 items ━━"},
		{"HTML", "2 articles"},
		{"HTML", "1 article"},
		{"HTML", "0 articles"},
	}
	for _, c := range checks {
		if !strings.Contains(surfaces[c.surface], c.snippet) {
			t.Errorf("%s: expected to find %q\n--- output ---\n%s",
				c.surface, c.snippet, surfaces[c.surface])
		}
	}

	// 3) Every surface must keep each item's headline strictly inside
	// its own field's section. URLs are no longer emitted by any
	// renderer in the new format — we verify field boundaries via
	// headline ordering instead.
	type headlineCheck struct {
		surface   string
		fieldName string
		headline  string
		nextField string // "" means last field
	}
	checks2 := []headlineCheck{
		// AI/ML's first headline must appear before Cricket's header.
		{"CLI", "AI/ML", "AI headline A", "Cricket"},
		{"Telegram", "AI/ML", "AI headline A", "Cricket"},
		{"HTML", "AI/ML", "AI headline A", "Cricket"},
		// Cricket's headline must appear after Cricket's header and before Gaming's.
		{"CLI", "Cricket", "Cricket headline A", "Gaming"},
		{"Telegram", "Cricket", "Cricket headline A", "Gaming"},
		{"HTML", "Cricket", "Cricket headline A", "Gaming"},
	}
	for _, c := range checks2 {
		out := surfaces[c.surface]
		fieldStart, nextStart := sectionBounds(c.surface, out, c.fieldName, c.nextField)
		if fieldStart < 0 {
			t.Errorf("%s: could not locate field header for %q", c.surface, c.fieldName)
			continue
		}
		headlineIdx := strings.Index(out, c.headline)
		if headlineIdx < 0 {
			t.Errorf("%s: headline %q not found in output", c.surface, c.headline)
			continue
		}
		if headlineIdx <= fieldStart {
			t.Errorf("%s: headline %q for field %q appears at offset %d, before its field header at %d",
				c.surface, c.headline, c.fieldName, headlineIdx, fieldStart)
		}
		if c.nextField != "" && nextStart >= 0 && headlineIdx >= nextStart {
			t.Errorf("%s: headline %q for field %q appears at offset %d, after the next field's header at %d",
				c.surface, c.headline, c.fieldName, headlineIdx, nextStart)
		}
	}
}

// sectionBounds returns (fieldHeaderStart, nextFieldHeaderStart) for
// a given field's section in a given surface's output. The caller
// uses the first return as the lower bound (the URL must be strictly
// after it) and the second return as the upper bound (the URL must
// be strictly before it). For the last field, pass nextField=""; the
// upper bound will be len(out).
func sectionBounds(surface, out, field, nextField string) (int, int) {
	switch surface {
	case "CLI":
		start := strings.Index(out, "FIELD: "+strings.ToUpper(field))
		var next int
		if nextField != "" {
			next = strings.Index(out, "FIELD: "+strings.ToUpper(nextField))
		}
		if next < 0 {
			next = len(out)
		}
		return start, next
	case "Telegram":
		start := strings.Index(out, "━━ "+strings.ToUpper(field))
		var next int
		if nextField != "" {
			next = strings.Index(out, "━━ "+strings.ToUpper(nextField))
		}
		if next < 0 {
			next = len(out)
		}
		return start, next
	case "HTML":
		// Find the <section> whose data-field-name attribute matches.
		fieldAttr := `data-field-name="` + field + `"`
		fieldAt := strings.Index(out, fieldAttr)
		if fieldAt < 0 {
			return -1, -1
		}
		sectionStart := strings.LastIndex(out[:fieldAt], "<section ")
		var next int
		if nextField != "" {
			nextAttr := `data-field-name="` + nextField + `"`
			nextAt := strings.Index(out, nextAttr)
			if nextAt >= 0 {
				next = strings.LastIndex(out[:nextAt], "<section ")
			}
		}
		if next < 0 {
			next = len(out)
		}
		return sectionStart, next
	}
	return -1, -1
}

// TestCrossSurface_HTMLIsSingleDocument checks that the HTML output is
// a single, well-formed block (one <div class="news-digest"> wrapper,
// never one-per-field), so the Web UI can set innerHTML on a single
// container and get the full digest.
func TestCrossSurface_HTMLIsSingleDocument(t *testing.T) {
	view := BuildDigestView(sampleDigestForTest())
	html := FormatHTMLView(view)

	if c := strings.Count(html, `<div class="news-digest"`); c != 1 {
		t.Errorf("expected exactly one <div class=\"news-digest\"> wrapper, got %d in:\n%s", c, html)
	}
}

func sampleDigestForTest() *NewsDigest {
	now := time.Now()
	return &NewsDigest{
		RunID:       1,
		Window:      "24h",
		GeneratedAt: now,
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "A", Items: []DigestItem{{Headline: "a"}}},
			{FieldID: 2, FieldName: "B", Items: []DigestItem{}},
		},
	}
}
