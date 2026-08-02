package news

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestCrossSurface_WindowMatrix (Phase 12) is the programmatic
// equivalent of the work.md "manual test matrix" item:
//
//   "real profile with 3+ fields, various duration phrases,
//    verify Web UI cards + Telegram sections + CLI output
//    all stay visually distinct per field"
//
// A live manual run covers duration phrases like "last 24h",
// "past 3 days", "past week", etc., with a multi-field
// profile, and walks the same digest through the three
// surfaces. This test replicates that matrix in CI: a
// table-driven loop over a representative set of windows,
// each producing a 3-field, multi-item digest rendered
// through CLI / Telegram / HTML, with the field-separation
// invariant checked on every (window, surface) combination.
//
// The original TestCrossSurface_FieldSeparation covers one
// window ("past 3 days"). This test is the missing
// parameterized version that proves the invariant is
// independent of the window phrase — that is, no matter
// what window the user typed, the three surfaces all
// preserve per-field boundaries.
func TestCrossSurface_WindowMatrix(t *testing.T) {
	now := time.Now()
	pubA := now.Add(-2 * time.Hour)
	pubB := now.Add(-5 * time.Hour)
	pubC := now.Add(-90 * time.Minute)
	pubD := now.Add(-15 * time.Hour)

	// Each row is one row of the manual matrix: a window
	// phrase + a 3-field digest with items tagged by field
	// name. The digest is identical for every (window, field)
	// pair except the window phrase and the item counts /
	// URLs — the field-separation invariant is about
	// boundaries, not about items changing shape per window.
	windows := []struct {
		window string
		// itemsPerField[name] = number of items to put in that
		// field. Lets the matrix cover "0 items" (empty
		// field), "1 item" (typical), "2 items" (heavy).
		itemsPerField map[string]int
	}{
		{window: "last 24h", itemsPerField: map[string]int{"AI/ML": 2, "Cricket": 1, "Gaming": 0}},
		{window: "past 3 days", itemsPerField: map[string]int{"AI/ML": 1, "Cricket": 2, "Gaming": 1}},
		{window: "past week", itemsPerField: map[string]int{"AI/ML": 0, "Cricket": 1, "Gaming": 2}},
		{window: "1d", itemsPerField: map[string]int{"AI/ML": 2, "Cricket": 0, "Gaming": 1}},
		{window: "7d", itemsPerField: map[string]int{"AI/ML": 1, "Cricket": 1, "Gaming": 1}},
	}

	// Build a 3-field digest template; per-window tests
	// produce a fresh digest by selecting from this.
	templateFields := []FieldDigest{
		{FieldID: 1, FieldName: "AI/ML", PriorityOrder: 1, Items: []DigestItem{
			{Headline: "AI headline one", URL: "https://example.com/ai/1", Source: "Tech", PublishedAt: &pubA, FetchIntegrity: "ok"},
			{Headline: "AI headline two", URL: "https://example.com/ai/2", Source: "Tech", PublishedAt: &pubB, FetchIntegrity: "ok"},
		}},
		{FieldID: 2, FieldName: "Cricket", PriorityOrder: 2, Items: []DigestItem{
			{Headline: "Cricket headline one", URL: "https://example.com/cricket/1", Source: "ESPN", PublishedAt: &pubC, FetchIntegrity: "ok"},
			{Headline: "Cricket headline two", URL: "https://example.com/cricket/2", Source: "ESPN", PublishedAt: &pubD, FetchIntegrity: "ok"},
		}},
		{FieldID: 3, FieldName: "Gaming", PriorityOrder: 3, Items: []DigestItem{
			{Headline: "Gaming headline one", URL: "https://example.com/gaming/1", Source: "IGN", PublishedAt: &pubA, FetchIntegrity: "ok"},
			{Headline: "Gaming headline two", URL: "https://example.com/gaming/2", Source: "IGN", PublishedAt: &pubB, FetchIntegrity: "ok"},
		}},
	}

	buildDigest := func(window string, itemsPerField map[string]int) *NewsDigest {
		fields := make([]FieldDigest, 0, len(templateFields))
		for _, fld := range templateFields {
			n := itemsPerField[fld.FieldName]
			if n > len(fld.Items) {
				n = len(fld.Items)
			}
			newField := FieldDigest{
				FieldID:       fld.FieldID,
				FieldName:     fld.FieldName,
				PriorityOrder: fld.PriorityOrder,
				Items:         fld.Items[:n],
			}
			fields = append(fields, newField)
		}
		return &NewsDigest{
			RunID:       1,
			ProfileID:   1,
			Window:      window,
			GeneratedAt: now,
			Fields:      fields,
		}
	}

	// Item-count phrase is per-surface because the three
	// formatters use slightly different unit names ("item"
	// vs "article"). This is the Phase 9 contract:
	// every surface shows the resolved count, but the unit
	// word is surface-appropriate. We assert on the
	// surface's own unit, not a single shared phrase.
	countPhraseFor := func(surface string, n int) string {
		if n == 1 {
			if surface == "HTML" {
				return "1 article"
			}
			return "1 item"
		}
		if n == 0 {
			return "" // empty state has its own copy, handled below
		}
		if surface == "HTML" {
			return fmt.Sprintf("%d articles", n)
		}
		return fmt.Sprintf("%d items", n)
	}

	containsCI := func(haystack, needle string) bool {
		return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
	}

	for _, w := range windows {
		w := w
		t.Run("window="+w.window, func(t *testing.T) {
			d := buildDigest(w.window, w.itemsPerField)
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

			// 1) Every surface must show the resolved window.
			// The user typed "past 3 days" — every surface
			// must reflect that.
			for name, out := range surfaces {
				if !strings.Contains(out, w.window) {
					t.Errorf("%s: missing window %q\n--- output ---\n%s",
						name, w.window, out)
				}
			}

			// 2) Every surface must mention every field name
			// (case-insensitive — CLI/Telegram uppercase).
			fieldNames := []string{"AI/ML", "Cricket", "Gaming"}
			for name, out := range surfaces {
				for _, f := range fieldNames {
					if !containsCI(out, f) {
						t.Errorf("%s: missing field name %q\n--- output ---\n%s",
							name, f, out)
					}
				}
			}

			// 3) Per-field headline containment: every headline
			// that belongs to a field's items must appear inside
			// that field's section in every surface, never before
			// its own field header or after the next field's header.
			// URLs are no longer emitted by any renderer in the
			// new format; headlines are the correct proxy.
			hChecks := []struct {
				fieldName string
				headlines []string
			}{}
			for _, fld := range d.Fields {
				headlines := []string{}
				for _, it := range fld.Items {
					headlines = append(headlines, it.Headline)
				}
				hChecks = append(hChecks, struct {
					fieldName string
					headlines []string
				}{fieldName: fld.FieldName, headlines: headlines})
			}

			for surfaceName, out := range surfaces {
				for i, c := range hChecks {
					nextField := ""
					if i+1 < len(hChecks) {
						nextField = hChecks[i+1].fieldName
					}
					fieldStart, nextStart := sectionBounds(surfaceName, out, c.fieldName, nextField)
					if fieldStart < 0 {
						t.Errorf("%s: could not locate header for field %q in window %q",
							surfaceName, c.fieldName, w.window)
						continue
					}
					for _, headline := range c.headlines {
						headlineIdx := strings.Index(out, headline)
						if headlineIdx < 0 {
							t.Errorf("%s: headline %q (field %q, window %q) not found in output",
								surfaceName, headline, c.fieldName, w.window)
							continue
						}
						if headlineIdx <= fieldStart {
							t.Errorf("%s: headline %q for field %q appears at offset %d, before field header at %d (window %q)",
								surfaceName, headline, c.fieldName, headlineIdx, fieldStart, w.window)
						}
						if nextField != "" && nextStart >= 0 && headlineIdx >= nextStart {
							t.Errorf("%s: headline %q for field %q appears at offset %d, after next field's header at %d (window %q)",
								surfaceName, headline, c.fieldName, headlineIdx, nextStart, w.window)
						}
					}
				}
			}

			// 4) Item-count check: for fields with N items,
			// the surface must show the count. For 0 items
			// the "empty field" copy must appear.
			for _, fld := range d.Fields {
				n := len(fld.Items)
				if n == 0 {
					// Empty-field copy differs per surface.
					// CLI: "(No recent news items found for this field)"
					// Telegram: "No recent news items found"
					// HTML: "news-empty-field" class
					if !strings.Contains(cli, "(No recent news items found for this field)") {
						t.Errorf("CLI: empty-field copy missing for %q (window %q)", fld.FieldName, w.window)
					}
					if !strings.Contains(tg, "No recent news items found") {
						t.Errorf("Telegram: empty-field copy missing for %q (window %q)", fld.FieldName, w.window)
					}
					if !strings.Contains(html, `class="news-empty-field"`) {
						t.Errorf("HTML: empty-field class missing for %q (window %q)", fld.FieldName, w.window)
					}
				} else {
					for surfaceName, out := range surfaces {
						want := countPhraseFor(surfaceName, n)
						if !strings.Contains(out, want) {
							t.Errorf("%s: missing count %q for field %q (window %q)",
								surfaceName, want, fld.FieldName, w.window)
						}
					}
				}
			}

			// 5) The CLI surface must contain the per-field
			// banner text with the resolved window. This is
			// the explicit Phase 9 contract test extended
			// across the matrix.
			for _, fld := range d.Fields {
				want := strings.ToUpper(fld.FieldName)
				cliExpected := "FIELD: " + want + " · window: " + w.window + " ·"
				if !strings.Contains(cli, cliExpected) {
					t.Errorf("CLI: missing per-field header %q (window %q, field %q, full cli: %q)",
						cliExpected, w.window, fld.FieldName, cli)
				}
			}
		})
	}
}
