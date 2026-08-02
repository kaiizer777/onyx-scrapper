package news

import (
	"strings"
	"testing"
	"time"
)

// sampleDigest builds a multi-field digest used by the HTML formatter
// tests. It includes a populated field, an empty field, and a field
// with a long list — enough surface area to assert field separation,
// item count, and the new per-field window.
func sampleDigest() *NewsDigest {
	now := time.Now()
	pub1 := now.Add(-90 * time.Minute)
	pub2 := now.Add(-5 * time.Hour)
	return &NewsDigest{
		RunID:       99,
		ProfileID:   1,
		Window:      "past 3 days",
		GeneratedAt: now,
		Fields: []FieldDigest{
			{
				FieldID:       1,
				FieldName:     "AI/ML",
				PriorityOrder: 1,
				Items: []DigestItem{
					{
						Headline:       "New LLM released",
						Takeaway:       "Significantly better reasoning.",
						URL:            "https://news.example.com/llm",
						Source:         "Tech Daily",
						PublishedAt:    &pub1,
						FetchIntegrity: "ok",
					},
					{
						Headline:       "Open-source model update",
						Takeaway:       "Adds agentic capabilities.",
						URL:            "https://news.example.com/oss",
						Source:         "AI Weekly",
						PublishedAt:    &pub2,
						FetchIntegrity: "ok",
					},
				},
			},
			{
				FieldID:       2,
				FieldName:     "Gaming",
				PriorityOrder: 2,
				Items:         []DigestItem{},
			},
		},
	}
}

func TestFormatHTML_BasicStructure(t *testing.T) {
	out := FormatHTML(sampleDigest())

	// Phase 9: every field is a self-contained <section>.
	if c := strings.Count(out, `<section class="news-field-card"`); c != 2 {
		t.Errorf("expected 2 field-card sections, got %d in:\n%s", c, out)
	}
	// Digest-level wrapper with the run id.
	if !strings.Contains(out, `id="news-digest-99"`) {
		t.Errorf("expected digest wrapper id, got:\n%s", out)
	}
	// Each field header includes the resolved window.
	if !strings.Contains(out, "past 3 days") {
		t.Errorf("expected resolved window in output, got:\n%s", out)
	}
	// Items are inside the populated field, not the empty one.
	if c := strings.Count(out, `class="news-item-row"`); c != 2 {
		t.Errorf("expected 2 item rows, got %d in:\n%s", c, out)
	}
	// Empty field shows the empty-state element.
	if !strings.Contains(out, `class="news-empty-field"`) {
		t.Errorf("expected empty-field element, got:\n%s", out)
	}
}

func TestFormatHTML_FieldSeparation(t *testing.T) {
	out := FormatHTML(sampleDigest())

	aiIdx := strings.Index(out, `<section class="news-field-card" data-field-id="1"`)
	gamingIdx := strings.Index(out, `<section class="news-field-card" data-field-id="2"`)
	if aiIdx < 0 || gamingIdx < 0 {
		t.Fatalf("expected both AI and Gaming sections, got:\n%s", out)
	}
	if aiIdx >= gamingIdx {
		t.Errorf("expected AI section before Gaming section")
	}
	// New format: headline appears (not URL) in the item chip-row area.
	// "New LLM released" must be inside the AI section (before Gaming).
	llmHeadlineIdx := strings.Index(out, "New LLM released")
	if llmHeadlineIdx < 0 || llmHeadlineIdx <= aiIdx || llmHeadlineIdx >= gamingIdx {
		t.Errorf("LLM headline must appear within the AI section, got offsets ai=%d headline=%d gaming=%d",
			aiIdx, llmHeadlineIdx, gamingIdx)
	}
}

func TestFormatHTML_ItemMeta(t *testing.T) {
	out := FormatHTML(sampleDigest())

	// Source appears in the chip-row (uppercased).
	if !strings.Contains(out, "TECH DAILY") {
		t.Errorf("expected 'TECH DAILY' source in chip-row, got:\n%s", out)
	}
	// Item count text — populated field should say '2 articles' (plural).
	if !strings.Contains(out, "2 articles") {
		t.Errorf("expected '2 articles' in populated field header, got:\n%s", out)
	}
	// Empty field should say '0 articles'.
	if !strings.Contains(out, "0 articles") {
		t.Errorf("expected '0 articles' in empty field header, got:\n%s", out)
	}
	// Chip-row is present.
	if !strings.Contains(out, `class="news-item-chip-row"`) {
		t.Errorf("expected chip-row class in output, got:\n%s", out)
	}
	// Number chip is present.
	if !strings.Contains(out, `class="news-item-num"`) {
		t.Errorf("expected news-item-num chip in output, got:\n%s", out)
	}
	// Integrity chip is no longer emitted in the new format.
	if strings.Contains(out, "verified") {
		t.Errorf("'verified' chip must not appear in new format, got:\n%s", out)
	}
}

func TestFormatHTML_EscapesUserContent(t *testing.T) {
	d := &NewsDigest{
		RunID:  1,
		Window: "24h",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: `Evil <script>alert("x")</script>`, Items: []DigestItem{
				{
					Headline: `<img src=x onerror=alert(1)>`,
					Source:   `Tech & "Co"`,
					Body:     `Take <away> & 'quote'`,
				},
			}},
		},
	}
	out := FormatHTML(d)

	// Raw hostile content must NOT survive in the output.
	hostile := []string{
		`<script>alert`,
		`<img src=x onerror`,
		`"x"`,           // unescaped quote from field name
		`Tech & "Co"`,   // unescaped & between words
		`<away>`,        // unescaped body angle brackets
	}
	for _, h := range hostile {
		if strings.Contains(out, h) {
			t.Errorf("unescaped hostile content %q in output:\n%s", h, out)
		}
	}
	// Escaped entities must be present for the body text.
	if !strings.Contains(out, "Take &lt;away&gt;") {
		t.Errorf("expected escaped body text in output, got:\n%s", out)
	}
}

func TestFormatHTML_NilDigest(t *testing.T) {
	out := FormatHTML(nil)
	if !strings.Contains(out, "No news digest generated.") {
		t.Errorf("expected nil-digest message, got: %s", out)
	}
}

func TestFormatHTML_NoFields(t *testing.T) {
	d := &NewsDigest{RunID: 1, Window: "24h", Fields: []FieldDigest{}}
	out := FormatHTML(d)
	if !strings.Contains(out, "No profile fields") {
		t.Errorf("expected no-fields message, got: %s", out)
	}
}

func TestFormatHTMLField_Standalone(t *testing.T) {
	now := time.Now()
	pub := now.Add(-30 * time.Minute)
	fd := FieldDigest{
		FieldID:   7,
		FieldName: "Standalone",
		Items: []DigestItem{
			{
				Headline:    "Standalone item",
				URL:         "https://example.com/1",
				Source:      "Src",
				PublishedAt: &pub,
			},
		},
	}
	out := FormatHTMLField(fd)

	// Self-contained <section> with the right field id.
	if !strings.Contains(out, `data-field-id="7"`) {
		t.Errorf("expected data-field-id=7, got: %s", out)
	}
	if !strings.Contains(out, "Standalone item") {
		t.Errorf("expected headline in output, got: %s", out)
	}
	// New format: source link (href) is not emitted.
	if strings.Contains(out, `href="https://example.com/1"`) {
		t.Errorf("source link href must not appear in new format, got: %s", out)
	}
	// Chip-row with source name (uppercased) must be present.
	if !strings.Contains(out, "SRC") {
		t.Errorf("expected uppercased source 'SRC' in chip-row, got: %s", out)
	}
	if !strings.Contains(out, `class="news-item-chip-row"`) {
		t.Errorf("expected chip-row element, got: %s", out)
	}
}

func TestFormatHTML_HeadlineFromURL(t *testing.T) {
	// When headline is empty, the item should still render with the
	// URL as a fallback visible label.
	d := &NewsDigest{
		RunID:  1,
		Window: "24h",
		Fields: []FieldDigest{
			{FieldID: 1, FieldName: "AI", Items: []DigestItem{
				{URL: "https://example.com/fallback"},
			}},
		},
	}
	out := FormatHTML(d)
	if !strings.Contains(out, "https://example.com/fallback") {
		t.Errorf("expected URL as visible label fallback, got: %s", out)
	}
}
