package news

import (
	"fmt"
	"html"
	"strings"
)

// FormatHTML renders a NewsDigest as a single, safe HTML string. This
// is the cross-surface renderer for the Web UI (Phase 9): the browser
// drops the result straight into the chat thread via innerHTML.
//
// All user-supplied content (field names, headlines, sources, URLs,
// takeaways) is escaped for both text and attribute contexts. The
// output relies on the existing CSS classes in
// internal/webui/templates/index.html (.news-field-card,
// .news-field-header, .news-item-row, etc.) — it does not introduce
// new style hooks.
//
// Kept for backwards compatibility: callers that pass a raw
// NewsDigest still work. New code should call FormatHTMLView directly
// so the view is reused across surfaces.
func FormatHTML(digest *NewsDigest) string {
	return FormatHTMLView(BuildDigestView(digest))
}

// FormatHTMLView renders a DigestView as the full per-field HTML
// digest block, one <section class="news-field-card"> per field.
// The wrap element is <div class="news-digest" id="news-digest-NNN">.
//
// Phase 9 contract: every field is a self-contained <section>, never
// merged; every field header shows field name + resolved window + item
// count; every item shows headline, source, relative time, optional
// takeaway, and the full article body inline (so the reader does not
// have to click through to the source URL).
func FormatHTMLView(view *DigestView) string {
	if view == nil {
		return `<div class="news-digest empty">No news digest generated.</div>`
	}
	if len(view.Fields) == 0 {
		return `<div class="news-digest empty">No profile fields configured or processed.</div>`
	}

	var sb strings.Builder

	fmt.Fprintf(&sb, `<div class="news-digest" id="news-digest-%d">`, view.RunID)

	// Digest-level header. The CSS for `.news-digest-header` is a
	// flex row with gap, so the child spans render with proper
	// spacing ("News Digest" · "window: 7days" · "run #3 · 2
	// field(s) · 10 item(s)") instead of running together.
	sb.WriteString(`<header class="news-digest-header">`)
	fmt.Fprintf(&sb,
		`<span class="news-digest-icon">&#128240;</span>`+
			` <span class="news-digest-title">News Digest</span>`+
			` <span class="news-digest-window">window: %s</span>`+
			` <span class="news-digest-meta">run #%d &middot; %d field(s) &middot; %d item(s)</span>`,
		html.EscapeString(view.Window),
		view.RunID,
		len(view.Fields),
		totalItemCount(view),
	)
	sb.WriteString(`</header>`)

	// One self-contained <section> per field. Never merged.
	for _, fv := range view.Fields {
		sb.WriteString(FormatHTMLFieldView(fv))
	}

	sb.WriteString(`</div>`)
	return sb.String()
}

// FormatHTMLField renders a single FieldDigest as a self-contained
// <section class="news-field-card">. Kept for backwards compat — new
// code should call FormatHTMLFieldView.
func FormatHTMLField(fd FieldDigest) string {
	view := BuildDigestView(&NewsDigest{Fields: []FieldDigest{fd}})
	if len(view.Fields) == 0 {
		return ""
	}
	return FormatHTMLFieldView(view.Fields[0])
}

// FormatHTMLFieldView renders a single FieldView as the per-field
// <section> block consumed by the Web UI. Class names match the
// existing CSS in internal/webui/templates/index.html so no style
// changes are required.
func FormatHTMLFieldView(fv FieldView) string {
	var sb strings.Builder

	itemCountStr := fmt.Sprintf("%d article", fv.ItemCount)
	if fv.ItemCount != 1 {
		itemCountStr = fmt.Sprintf("%d articles", fv.ItemCount)
	}

	// <section class="news-field-card"> — the per-field card.
	fmt.Fprintf(&sb, `<section class="news-field-card" data-field-id="%d" data-field-name="%s">`,
		fv.FieldID, html.EscapeString(fv.FieldName))

	// Header: field name + window + item count. Window is the same
	// resolved phrase the run used, stamped onto the field by
	// BuildDigestView so every field's section is self-describing.
	sb.WriteString(`<header class="news-field-header">`)
	fmt.Fprintf(&sb,
		`<span class="news-field-header-icon">&#128240;</span>`+
			`<span class="news-field-name">%s</span>`+
			`<span class="news-field-window">%s</span>`+
			`<span class="news-field-count">%s</span>`,
		html.EscapeString(fv.FieldName),
		html.EscapeString(fv.Window),
		html.EscapeString(itemCountStr),
	)
	sb.WriteString(`</header>`)

	if fv.ItemCount == 0 {
		sb.WriteString(`<p class="news-empty-field">No articles found for this field.</p>`)
		sb.WriteString(`</section>`)
		return sb.String()
	}

	sb.WriteString(`<div class="news-field-items">`)
	for idx, item := range fv.Items {
		sb.WriteString(formatHTMLItem(idx, item))
	}
	sb.WriteString(`</div>`)

	sb.WriteString(`</section>`)
	return sb.String()
}

// formatHTMLItem renders a single news article in the new digest shape:
//
//	<article class="news-item-row" data-item-index="1">
//	  <div class="news-item-chip-row">
//	    <span class="news-item-num">#1</span>
//	    <span class="news-item-source">CIDRAP</span>
//	  </div>
//	  <h4 class="news-item-title">Headline text</h4>
//	  <div class="news-item-body"><p>2-3 sentence summary…</p></div>
//	</article>
//
// No .news-item-meta, no .news-item-source-link, no .news-item-summary.
func formatHTMLItem(idx int, item ItemView) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, `<article class="news-item-row" data-item-index="%d">`, idx+1)

	// Chip row: #N (blue badge) + SOURCE name (dim, uppercase).
	source := strings.ToUpper(strings.TrimSpace(item.Source))
	if source == "" {
		source = "UNKNOWN"
	}
	sb.WriteString(`<div class="news-item-chip-row">`)
	fmt.Fprintf(&sb, `<span class="news-item-num">#%d</span>`, idx+1)
	fmt.Fprintf(&sb, `<span class="news-item-source">%s</span>`, html.EscapeString(source))
	sb.WriteString(`</div>`)

	// Title — blue link-style heading.
	title := strings.TrimSpace(item.Headline)
	if title == "" {
		title = item.URL
	}
	fmt.Fprintf(&sb, `<h4 class="news-item-title">%s</h4>`, html.EscapeString(title))

	// Body — the LLM-short 2-3 sentence summary.
	bodyHTML := formatHTMLBody(item.Body, "")
	if bodyHTML != "" {
		fmt.Fprintf(&sb, `<div class="news-item-body">%s</div>`, bodyHTML)
	}

	sb.WriteString(`</article>`)
	return sb.String()
}


// formatHTMLBody returns the article body as safe HTML paragraphs.
// Double newlines split paragraphs; single newlines become spaces
// (the cleaned article text from the fetcher usually runs together
// without blank-line separation). If body is empty or duplicates
// the takeaway word-for-word, returns "" so the UI does not show
// the same paragraph twice.
func formatHTMLBody(body, takeaway string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if strings.TrimSpace(takeaway) != "" && strings.EqualFold(strings.TrimSpace(body), strings.TrimSpace(takeaway)) {
		return ""
	}
	// Collapse runs of whitespace (including \n, \r, \t) into
	// single spaces, then split on two-or-more-newline
	// boundaries via a small state machine so we don't lose
	// paragraph structure.
	normalized := strings.ReplaceAll(body, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	paragraphs := strings.Split(normalized, "\n\n")

	var sb strings.Builder
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Within a paragraph, collapse stray newlines to spaces.
		p = strings.Join(strings.Fields(p), " ")
		fmt.Fprintf(&sb, `<p>%s</p>`, html.EscapeString(p))
	}
	return sb.String()
}

func totalItemCount(view *DigestView) int {
	n := 0
	for _, fv := range view.Fields {
		n += fv.ItemCount
	}
	return n
}
