package news

import (
	"fmt"
	"strings"
	"time"
)

// ansi color helpers. Used only by the colorized CLI renderer so piped
// output stays clean. Windows 10+ terminals and modern Unix terminals
// handle these; older cmd.exe does not — the caller is responsible for
// gating the color path on a TTY.
const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiCyan    = "\x1b[36m"
	ansiMagenta = "\x1b[35m"
	ansiYellow  = "\x1b[33m"
	ansiGreen   = "\x1b[32m"
	ansiRed     = "\x1b[31m"
)

// FormatCLI builds a human-readable, sectioned CLI string representation
// of a NewsDigest. It is a thin wrapper kept for backwards compatibility
// with callers that pass the raw data-layer NewsDigest. New code should
// prefer FormatCLIView(view *DigestView) so the per-field window and
// pre-computed item counts are honored.
func FormatCLI(digest *NewsDigest) string {
	return FormatCLIView(BuildDigestView(digest), false)
}

// FormatCLIWithColor is FormatCLI with ANSI color emphasis. The CLI
// command should call this only when stdout is a TTY.
func FormatCLIWithColor(digest *NewsDigest) string {
	return FormatCLIView(BuildDigestView(digest), true)
}

// FormatCLIView renders a DigestView as a sectioned, colorized-or-plain
// CLI string. Phase 9 guarantees that every field section shows the
// field name, the resolved window, and the item count — never merged.
func FormatCLIView(view *DigestView, color bool) string {
	if view == nil {
		return "No news digest generated."
	}

	var sb strings.Builder

	// Top banner.
	sb.WriteString(strings.Repeat("=", 70) + "\n")
	if color {
		sb.WriteString(fmt.Sprintf("%s%sONYX NEWS DIGEST%s (Run ID: %d | Window: %s)\n",
			ansiBold, ansiCyan, ansiReset, view.RunID, view.Window))
	} else {
		sb.WriteString(fmt.Sprintf("ONYX NEWS DIGEST (Run ID: %d | Window: %s)\n", view.RunID, view.Window))
	}
	if !view.GeneratedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("Generated: %s\n", view.GeneratedAt.Format("2006-01-02 15:04:05 MST")))
	}
	sb.WriteString(strings.Repeat("=", 70) + "\n\n")

	if len(view.Fields) == 0 {
		sb.WriteString("No profile fields configured or processed.\n")
		return sb.String()
	}

	for i, f := range view.Fields {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(FormatCLIFieldView(f, color))
	}

	return sb.String()
}

// FormatCLIField builds a section string for a single profile field in
// the digest. Kept for backwards compatibility — it accepts the
// data-layer FieldDigest and routes through FormatCLIFieldView.
func FormatCLIField(fd FieldDigest) string {
	view := BuildDigestView(&NewsDigest{Fields: []FieldDigest{fd}})
	if len(view.Fields) == 0 {
		return ""
	}
	return FormatCLIFieldView(view.Fields[0], false)
}

// FormatCLIFieldView renders a single FieldView as a sectioned CLI
// block. Phase 9 requires the header to show the field name, the
// resolved window, and the item count.
func FormatCLIFieldView(fv FieldView, color bool) string {
	var sb strings.Builder

	itemCountStr := fmt.Sprintf("%d item", fv.ItemCount)
	if fv.ItemCount != 1 {
		itemCountStr = fmt.Sprintf("%d items", fv.ItemCount)
	}

	// Phase 9 header: FIELD · window · N items
	headerText := fmt.Sprintf("FIELD: %s · window: %s · %s",
		strings.ToUpper(fv.FieldName), fv.Window, itemCountStr)

	dividerWidth := 70
	if color {
		// Bold + cyan to make the field stand out from item body text.
		sb.WriteString(fmt.Sprintf("%s%s%s\n", ansiBold, ansiCyan, strings.Repeat("━", dividerWidth)))
		sb.WriteString(fmt.Sprintf("%s%s%s\n", ansiBold, ansiCyan, headerText))
		sb.WriteString(fmt.Sprintf("%s%s%s\n", ansiBold, ansiCyan, strings.Repeat("━", dividerWidth)))
	} else {
		sb.WriteString(strings.Repeat("━", dividerWidth) + "\n")
		sb.WriteString(headerText + "\n")
		sb.WriteString(strings.Repeat("━", dividerWidth) + "\n")
	}

	if fv.ItemCount == 0 {
		if color {
			sb.WriteString(fmt.Sprintf("  %s%s(No recent news items found for this field)%s\n", ansiDim, ansiYellow, ansiReset))
		} else {
			sb.WriteString("  (No recent news items found for this field)\n")
		}
		return sb.String()
	}

	for idx, item := range fv.Items {
		source := strings.ToUpper(strings.TrimSpace(item.Source))
		if source == "" {
			source = "UNKNOWN"
		}
		headline := strings.TrimSpace(item.Headline)
		if headline == "" {
			headline = item.URL
		}

		// Header: N. SOURCE — HEADLINE
		if color {
			sb.WriteString(fmt.Sprintf("\n%s%d. %s%s — %s\n", ansiBold, idx+1, source, ansiReset, headline))
		} else {
			sb.WriteString(fmt.Sprintf("\n%d. %s — %s\n", idx+1, source, headline))
		}

		// Body — the LLM-short 2-3 sentence summary, indented.
		body := strings.TrimSpace(item.Body)
		if body != "" {
			sb.WriteString("\n")
			// Word-wrap at ~70 cols with a 3-space indent.
			for _, line := range wrapText(body, 67) {
				sb.WriteString("   " + line + "\n")
			}
		}
	}

	return sb.String()
}

func formatRelativeTime(t time.Time) string {
	diff := time.Since(t)
	if diff < 0 {
		diff = -diff
	}
	if diff < time.Minute {
		return "just now"
	}
	if diff < time.Hour {
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	}
	if diff < 24*time.Hour {
		hrs := int(diff.Hours())
		if hrs == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hrs)
	}
	days := int(diff.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

// wrapText wraps the text at maxWidth runes, splitting on word boundaries.
// Returns a slice of lines. Never panics on empty input.
func wrapText(text string, maxWidth int) []string {
	if maxWidth <= 0 {
		maxWidth = 70
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	var line strings.Builder
	for _, w := range words {
		if line.Len() == 0 {
			line.WriteString(w)
		} else if line.Len()+1+len(w) <= maxWidth {
			line.WriteByte(' ')
			line.WriteString(w)
		} else {
			lines = append(lines, line.String())
			line.Reset()
			line.WriteString(w)
		}
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	return lines
}

// FormatTelegramDigestHeader builds the ack/header line sent before the
// per-field messages. Uses Telegram HTML parse mode tags.
func FormatTelegramDigestHeader(digest *NewsDigest) string {
	return FormatTelegramHeaderView(BuildDigestView(digest))
}

// FormatTelegramHeaderView renders the digest-level header for Telegram
// delivery. Uses HTML parse mode (bold, code).
func FormatTelegramHeaderView(view *DigestView) string {
	if view == nil {
		return "📰 <b>Onyx News Digest</b>"
	}
	return fmt.Sprintf("📰 <b>Onyx News Digest</b> — window: <code>%s</code> | run #%d | %d field(s)",
		htmlEscape(view.Window), view.RunID, len(view.Fields))
}

// FormatTelegramField renders a single field section for Telegram
// delivery. Kept for backwards compatibility — it accepts the
// data-layer FieldDigest and routes through FormatTelegramFieldView.
//
// The window phrase is required: the Phase 9 contract is that
// every field section's header shows the resolved window, and
// without it the header line would be "━━ NAME · window:  · N
// items ━━" — useless to the reader. The delivery path in the
// Telegram gateway calls this function once per field, so the
// window has to be passed by the caller (it lives on the parent
// NewsDigest, not on the per-field digest).
//
// Phase 12 invariant: this function MUST take the window as a
// parameter. The previous single-arg signature silently produced
// empty-window headers because BuildDigestView was called on a
// synthetic one-field digest with Window="". The e2e test
// TestRouter_News_EndToEnd_DeliveryAndFieldSeparation in the
// telegram package is the regression guard.
func FormatTelegramField(fd FieldDigest, window string) string {
	view := BuildDigestView(&NewsDigest{Window: window, Fields: []FieldDigest{fd}})
	if len(view.Fields) == 0 {
		return ""
	}
	return FormatTelegramFieldView(view.Fields[0])
}

// FormatTelegramFieldView renders a single FieldView as a Telegram HTML
// message body. The output is designed for chunkMessage — the caller
// splits at 4000-char boundaries if needed. Phase 9 requires the header
// to show field name + window + item count.
func FormatTelegramFieldView(fv FieldView) string {
	var sb strings.Builder

	itemCountStr := fmt.Sprintf("%d item", fv.ItemCount)
	if fv.ItemCount != 1 {
		itemCountStr = fmt.Sprintf("%d items", fv.ItemCount)
	}

	// Phase 9 header: ━━ NAME · window: <window> · N items ━━
	sb.WriteString(fmt.Sprintf("<b>━━ %s · window: %s · %s ━━</b>\n",
		htmlEscape(strings.ToUpper(fv.FieldName)),
		htmlEscape(fv.Window),
		itemCountStr))

	if fv.ItemCount == 0 {
		sb.WriteString("  <i>(No recent news items found for this field)</i>\n")
		return sb.String()
	}

	for idx, item := range fv.Items {
		source := strings.ToUpper(strings.TrimSpace(item.Source))
		if source == "" {
			source = "UNKNOWN"
		}
		headline := strings.TrimSpace(item.Headline)
		if headline == "" {
			headline = item.URL
		}

		// Header: <b>N. SOURCE</b> — HEADLINE
		sb.WriteString(fmt.Sprintf("\n<b>%d. %s</b> — %s\n",
			idx+1, htmlEscape(source), htmlEscape(headline)))

		// Body — the LLM-short 2-3 sentence summary, indented.
		body := strings.TrimSpace(item.Body)
		if body != "" {
			sb.WriteString("\n   " + htmlEscape(body) + "\n")
		}
	}

	return sb.String()
}

// htmlEscape replaces the five characters that Telegram HTML parse mode
// treats as special. We do NOT use html.EscapeString because that also
// escapes single-quotes which Telegram does not require and which can
// corrupt display.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// fallbackTakeawayFromSummary returns a short, single-line takeaway
// derived from a persisted store.NewsItem.Summary. Mirrors the
// summarizer's fallback path so the post-run HTML view is consistent
// with what the user saw when the run was live.
//
// Empty input → empty output. Long input is truncated at 200 chars
// with an ellipsis, matching the summarizer's behavior in
// fallbackTakeaway.
func fallbackTakeawayFromSummary(summary string) string {
	s := strings.TrimSpace(summary)
	if s == "" {
		return ""
	}
	// Collapse newlines so the takeaway is one line in the digest.
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	// Collapse repeated whitespace.
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		// Don't cut a word in half.
		if idx := strings.LastIndex(s[:200], " "); idx > 80 {
			s = s[:idx]
		} else {
			s = s[:200]
		}
		s += "..."
	}
	return s
}
