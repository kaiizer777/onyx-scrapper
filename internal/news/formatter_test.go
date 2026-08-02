package news

import (
	"strings"
	"testing"
	"time"
)

func TestFormatCLI(t *testing.T) {
	now := time.Now()
	pubTime := now.Add(-2 * time.Hour)

	digest := &NewsDigest{
		RunID:       42,
		ProfileID:   1,
		Window:      "past 3 days",
		GeneratedAt: now,
		Fields: []FieldDigest{
			{
				FieldID:       10,
				FieldName:     "AI/ML",
				PriorityOrder: 1,
				Items: []DigestItem{
					{
						Headline:       "New LLM Model Released",
						Takeaway:       "Significant boost in reasoning and math performance.",
						URL:            "https://news.example.com/llm-model",
						Source:         "Tech Daily",
						PublishedAt:    &pubTime,
						FetchIntegrity: "high",
					},
				},
			},
			{
				FieldID:       11,
				FieldName:     "Gaming",
				PriorityOrder: 2,
				Items:         []DigestItem{},
			},
		},
	}

	output := FormatCLI(digest)

	if !strings.Contains(output, "ONYX NEWS DIGEST (Run ID: 42 | Window: past 3 days)") {
		t.Errorf("Expected banner with Run ID and Window, got:\n%s", output)
	}

	// Phase 9: every field section header must show field name + window + item count.
	if !strings.Contains(output, "FIELD: AI/ML \u00b7 window: past 3 days \u00b7 1 item") {
		t.Errorf("Expected AI/ML section header with window + count, got:\n%s", output)
	}

	// New shape: N. SOURCE — HEADLINE (source uppercased, no Takeaway/URL lines).
	if !strings.Contains(output, "1. TECH DAILY — New LLM Model Released") {
		t.Errorf("Expected new item shape '1. SOURCE — HEADLINE', got:\n%s", output)
	}

	// Old Takeaway/URL lines are removed from the new format.
	if strings.Contains(output, "Takeaway:") {
		t.Errorf("Takeaway prefix must not appear in new format, got:\n%s", output)
	}
	if strings.Contains(output, "URL:") {
		t.Errorf("URL prefix must not appear in new format, got:\n%s", output)
	}

	if !strings.Contains(output, "FIELD: GAMING \u00b7 window: past 3 days \u00b7 0 items") {
		t.Errorf("Expected Gaming section header with window + 0 items, got:\n%s", output)
	}

	if !strings.Contains(output, "(No recent news items found for this field)") {
		t.Errorf("Expected empty state message for Gaming field, got:\n%s", output)
	}
}

func TestFormatCLINil(t *testing.T) {
	output := FormatCLI(nil)
	if output != "No news digest generated." {
		t.Errorf("Expected nil digest message, got %q", output)
	}
}

func TestFormatCLINoFields(t *testing.T) {
	digest := &NewsDigest{
		RunID:  1,
		Window: "24h",
		Fields: []FieldDigest{},
	}
	output := FormatCLI(digest)
	if !strings.Contains(output, "No profile fields configured or processed.") {
		t.Errorf("Expected no fields message, got:\n%s", output)
	}
}

// TestFormatCLI_FieldSeparation asserts Phase 9's most important
// invariant: no field's items are ever merged into another field's
// section. The CLI output must contain each field name as its own
// header line, and an item's headline must appear after its own
// field's header — never before another field's header.
func TestFormatCLI_FieldSeparation(t *testing.T) {
	digest := &NewsDigest{
		RunID:  1,
		Window: "24h",
		Fields: []FieldDigest{
			{
				FieldID:   1,
				FieldName: "AI",
				Items: []DigestItem{
					{Headline: "AI item", URL: "https://a.example/1"},
				},
			},
			{
				FieldID:   2,
				FieldName: "Sports",
				Items: []DigestItem{
					{Headline: "Sports item", URL: "https://s.example/1"},
				},
			},
		},
	}
	out := FormatCLI(digest)

	// Two distinct field headers, one per field.
	aiIdx := strings.Index(out, "FIELD: AI ")
	sportsIdx := strings.Index(out, "FIELD: SPORTS")
	if aiIdx < 0 || sportsIdx < 0 {
		t.Fatalf("expected both AI and SPORTS field headers, got:\n%s", out)
	}
	if aiIdx >= sportsIdx {
		t.Errorf("expected AI section before SPORTS section")
	}
	// New format: items show as "N. SOURCE — HEADLINE". Verify headline ordering.
	aiItemIdx := strings.Index(out, "AI item")
	if aiItemIdx < 0 || aiItemIdx > sportsIdx {
		t.Errorf("AI headline must appear within the AI section (before SPORTS header), got:\n%s", out)
	}
	sportsItemIdx := strings.Index(out, "Sports item")
	if sportsItemIdx < 0 || sportsItemIdx < sportsIdx {
		t.Errorf("Sports headline must appear within the Sports section (after SPORTS header), got:\n%s", out)
	}
	// URLs are no longer emitted by the new CLI format.
	if strings.Contains(out, "https://a.example/1") || strings.Contains(out, "https://s.example/1") {
		t.Errorf("URLs must not appear in new CLI format, got:\n%s", out)
	}
}

// TestFormatCLI_ColorPath verifies the colorized variant still
// contains the same Phase-9 contract (field name, window, count) and
// emits ANSI escape codes that won't appear in the plain path.
func TestFormatCLI_ColorPath(t *testing.T) {
	digest := &NewsDigest{
		RunID:  9,
		Window: "past week",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "AI/ML", Items: []DigestItem{
				{Headline: "H", URL: "https://x"},
			}},
		},
	}
	out := FormatCLIWithColor(digest)
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("expected ANSI escape codes in colorized output, got:\n%s", out)
	}
	if !strings.Contains(out, "FIELD: AI/ML · window: past week · 1 item") {
		t.Errorf("colorized output must preserve field/window/count contract, got:\n%s", out)
	}
}

// ---------- Telegram formatter tests ----------

func TestFormatTelegramField_Empty(t *testing.T) {
	fd := FieldDigest{
		FieldID:   1,
		FieldName: "Gaming",
		Items:     []DigestItem{},
	}
	out := FormatTelegramField(fd, "24h")
	if !strings.Contains(out, "<b>") {
		t.Errorf("expected HTML bold tag in output, got: %s", out)
	}
	if !strings.Contains(out, "GAMING") {
		t.Errorf("expected uppercased field name, got: %s", out)
	}
	if !strings.Contains(out, "0 items") {
		t.Errorf("expected '0 items', got: %s", out)
	}
	if !strings.Contains(out, "No recent news items found") {
		t.Errorf("expected empty-state message, got: %s", out)
	}
	// Phase 9: Telegram field header must include the resolved window.
	if !strings.Contains(out, "window:") {
		t.Errorf("expected 'window:' in Telegram field header, got: %s", out)
	}
}

func TestFormatTelegramField_SingleItem(t *testing.T) {
	pubTime := time.Now().Add(-3 * time.Hour)
	fd := FieldDigest{
		FieldID:   10,
		FieldName: "AI/ML",
		Items: []DigestItem{
			{
				Headline:       "GPT-5 Announced",
				Takeaway:       "Major leap in reasoning.",
				Body:           "GPT-5 represents a major leap in reasoning capability.",
				URL:            "https://example.com/gpt5",
				Source:         "Tech News",
				PublishedAt:    &pubTime,
				FetchIntegrity: "ok",
			},
		},
	}
	out := FormatTelegramField(fd, "past 24h")

	if !strings.Contains(out, "1 item") {
		t.Errorf("expected '1 item' (singular), got: %s", out)
	}
	if !strings.Contains(out, "GPT-5 Announced") {
		t.Errorf("expected headline in output, got: %s", out)
	}
	// New shape: source uppercased in the item header, not as a meta line.
	if !strings.Contains(out, "TECH NEWS") {
		t.Errorf("expected uppercased source in output, got: %s", out)
	}
	// Body should appear (it's set on the DigestItem).
	if !strings.Contains(out, "GPT-5 represents a major leap") {
		t.Errorf("expected body text in output, got: %s", out)
	}
	// New format: takeaway italic and URL are NOT emitted.
	if strings.Contains(out, "https://example.com/gpt5") {
		t.Errorf("URL must not appear in new Telegram format, got: %s", out)
	}
	if strings.Contains(out, "<i>") {
		t.Errorf("italic tag for takeaway must not appear in new format, got: %s", out)
	}
	// HTML bold tags still present (for the item header).
	if !strings.Contains(out, "<b>") || !strings.Contains(out, "</b>") {
		t.Errorf("expected HTML bold tags, got: %s", out)
	}
	// Phase 9: window appears in field header.
	if !strings.Contains(out, "window:") {
		t.Errorf("expected 'window:' in Telegram field header, got: %s", out)
	}
}

func TestFormatTelegramField_HTMLEscaping(t *testing.T) {
	fd := FieldDigest{
		FieldID:   99,
		FieldName: "Test <field> & more",
		Items:     []DigestItem{},
	}
	out := FormatTelegramField(fd, "24h")
	// Raw < > & must not appear in the output (they should be escaped)
	if strings.Contains(out, "<field>") {
		t.Errorf("unescaped '<field>' found in output: %s", out)
	}
	if strings.Contains(out, "& more") && !strings.Contains(out, "&amp;") {
		t.Errorf("unescaped '&' found in output: %s", out)
	}
}

func TestFormatTelegramDigestHeader(t *testing.T) {
	digest := &NewsDigest{
		RunID:  7,
		Window: "past 24h",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "A", Items: []DigestItem{}},
			{FieldID: 2, FieldName: "B", Items: []DigestItem{}},
		},
	}
	out := FormatTelegramDigestHeader(digest)
	if !strings.Contains(out, "run #7") {
		t.Errorf("expected run ID in header, got: %s", out)
	}
	if !strings.Contains(out, "past 24h") {
		t.Errorf("expected window in header, got: %s", out)
	}
	if !strings.Contains(out, "📰") {
		t.Errorf("expected emoji in header, got: %s", out)
	}
	// Phase 9: digest-level header should report the number of fields
	// so the user knows how many sections to expect.
	if !strings.Contains(out, "2 field") {
		t.Errorf("expected '2 field(s)' in header, got: %s", out)
	}
}

func TestFormatTelegramDigestHeader_Nil(t *testing.T) {
	out := FormatTelegramDigestHeader(nil)
	if out == "" {
		t.Error("expected non-empty output for nil digest")
	}
}

func TestHtmlEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a & b", "a &amp; b"},
		{"<script>", "&lt;script&gt;"},
		{"a < b > c", "a &lt; b &gt; c"},
	}
	for _, tc := range cases {
		got := htmlEscape(tc.in)
		if got != tc.want {
			t.Errorf("htmlEscape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

