package telegram

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Telegram formatting limits. These are the canonical "hard" numbers —
// see https://core.telegram.org/bots/api#sendmessage and
// https://core.telegram.org/bots/api#senddocument. Keep them here so
// every formatter/chunker references the same constants.
const (
	// MaxMessageChars is Telegram's hard cap on a single text message.
	MaxMessageChars = 4096
	// MaxCaptionChars is the caption cap on photos/documents — used
	// when we fall back to a file upload with a one-line summary.
	MaxCaptionChars = 1024
	// FileFallbackThreshold is the body length above which we switch
	// from "send as a series of messages" to "send as a single
	// document". Setting this to MaxMessageChars ensures we never chunk,
	// and instead immediately fall back to a file attachment for large reports.
	FileFallbackThreshold = MaxMessageChars
	// CodeBlockInlineCap is the threshold above which fenced code
	// blocks (e.g. JSON extraction results) are attached as files
	// instead of inlined. Telegram renders >1000 char code blocks
	// poorly; a file is far more useful.
	CodeBlockInlineCap = 1000
)

// formatter is the package-internal helper that converts Onyx
// artefacts (report markdown, JSON extraction results, source citation
// lists) into Telegram-ready HTML or files. It is intentionally
// stateless and side-effect-free: callers own the network Send. That
// makes every code path unit-testable without a mock server.
type formatter struct{}

// newFormatter returns the package-default formatter. The struct
// exists so tests can extend it later (e.g. for cache or per-locale
// punctuation) without changing the call sites.
func newFormatter() *formatter { return &formatter{} }

// MarkdownToHTML converts Onyx's `report_md` (CommonMark-ish, possibly
// containing raw `**bold**`, `[label](url)`, fenced code, `> blockquotes`)
// into a Telegram-safe HTML representation. The conversion is lossy by
// design: anything we can't represent cleanly is escaped. We deliberately
// pick HTML over MarkdownV2 because (a) HTML only requires escaping `&`,
// `<`, `>`, and `"` — no reserved-character explosion, and (b) the
// citation list at the end (Phase 8) is naturally `<a href>` so HTML
// is the right default.
//
// The function never panics on weird input — bad links fall back to
// plain text, code spans lose their fences but keep their content,
// etc.
func (f *formatter) MarkdownToHTML(md string) string {
	if md == "" {
		return ""
	}
	// Normalize line endings so Windows-clipped source pastes don't
	// confuse later regex passes.
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md = strings.ReplaceAll(md, "\r", "\n")

	// Process line-by-line. Fenced code blocks get extracted to their
	// own "<pre>" segments (Phase 8) and re-attached at the end so we
	// don't mangle the contents of code spans.
	var (
		out          strings.Builder
		inCode       bool
		codeBuf      strings.Builder
		codeLang     string
	)
	flushCode := func() {
		if codeBuf.Len() == 0 {
			return
		}
		out.WriteString(f.renderCodeBlock(codeBuf.String(), codeLang, false))
		codeBuf.Reset()
		codeLang = ""
	}
	lines := strings.Split(md, "\n")
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		// Fenced code block: ``` or ```lang
		if strings.HasPrefix(trimmed, "```") {
			if !inCode {
				flushCode() // close any prior (shouldn't happen, defensive)
				inCode = true
				// language is whatever follows the opening fence
				lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				codeLang = lang
				codeBuf.Reset()
				continue
			}
			// closing fence — emit the block
			out.WriteString(f.renderCodeBlock(codeBuf.String(), codeLang, false))
			inCode = false
			codeLang = ""
			codeBuf.Reset()
			continue
		}
		if inCode {
			codeBuf.WriteString(line)
			codeBuf.WriteByte('\n')
			continue
		}
		out.WriteString(f.renderLine(trimmed))
		out.WriteByte('\n')
	}
	// Trailing unclosed code block — render whatever we buffered.
	if inCode && codeBuf.Len() > 0 {
		out.WriteString(f.renderCodeBlock(codeBuf.String(), codeLang, true))
	}
	// Trim trailing newline.
	res := out.String()
	res = strings.TrimRight(res, "\n")
	return res
}

// renderLine handles a single non-code line: headers, blockquotes,
// bullets, bold/italic, and inline links.
func (f *formatter) renderLine(line string) string {
	// Headers: #, ##, ### ...  -> <b>...</b> (Telegram has no
	// native H1/H2; bold is the closest visual match and keeps the
	// text scannable in a chat).
	if strings.HasPrefix(line, "#") {
		trimmed := strings.TrimLeft(line, "#")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			return ""
		}
		return "<b>" + inlineMarkdown(trimmed) + "</b>"
	}
	// Blockquote: leading "> " (Markdown standard). HTML equivalent
	// is <i> with a leading em-space to set it off.
	if strings.HasPrefix(line, "&gt; ") || strings.HasPrefix(line, "> ") {
		trimmed := strings.TrimPrefix(line, "> ")
		trimmed = strings.TrimPrefix(trimmed, "&gt; ")
		trimmed = strings.TrimSpace(trimmed)
		return "<i>" + inlineMarkdown(trimmed) + "</i>"
	}
	// Bulleted list: "- " or "* " at the start.
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
		trimmed := strings.TrimPrefix(line, "- ")
		trimmed = strings.TrimPrefix(trimmed, "* ")
		// U+2022 BULLET + NBSP keeps the bullet visible after HTML
		// parsing without relying on Telegram entity parsing.
		return "\u2022\u00a0" + inlineMarkdown(trimmed)
	}
	// Plain text.
	return inlineMarkdown(line)
}

// inlineMarkdown handles bold, italic, code, and links. We are
// intentionally conservative: unclosed markers (e.g. `**bold without
// close`) are passed through as literal asterisks, which Telegram
// will just show as text.
func inlineMarkdown(s string) string {
	if s == "" {
		return ""
	}
	// Bold: **text**
	s = replaceDelimited(s, "**", "<b>", "</b>")
	// Italic: _text_ or *text*  (Markdown flavour)
	s = replaceDelimited(s, "__", "<i>", "</i>")
	// Inline code: `text`  -> <code>text</code>
	s = replaceDelimited(s, "`", "<code>", "</code>")
	// Links: [label](url)  -> <a href="url">label</a>
	s = renderLinks(s)
	// HTML-escape the leftovers so raw `&`, `<`, `>` in user-visible
	// text don't blow up Telegram's parser. We re-escape AFTER the
	// tag inserts above because we already produced real tags that
	// must NOT be re-escaped. renderLinks produces complete `<a>`
	// tags; bold/italic/code are produced from a clean state (no
	// `&<>` in the delim), so escaping the post-link string is safe.
	return htmlEscapeExceptTags(s)
}

// replaceDelimited finds all non-overlapping `open...close` pairs of
// the same delimiter and wraps the inside. Unmatched delimiters are
// left as literal characters. Operates left-to-right so nested
// markers (rare in our inputs) do not greedily consume each other.
func replaceDelimited(s, delim, openTag, closeTag string) string {
	if delim == "" {
		return s
	}
	var out strings.Builder
	for {
		start := strings.Index(s, delim)
		if start < 0 {
			out.WriteString(s)
			return out.String()
		}
		// Look for the next occurrence of the same delimiter AFTER
		// the current one. Empty content between delimiters is
		// passed through (no tag inserted).
		rest := s[start+len(delim):]
		end := strings.Index(rest, delim)
		if end < 0 {
			// Unmatched — emit the rest as literal text and stop.
			out.WriteString(s)
			return out.String()
		}
		// Emit everything before the open delimiter.
		out.WriteString(s[:start])
		// Wrap the content. Empty content is allowed (renders as
		// adjacent open+close tags which Telegram tolerates).
		inner := rest[:end]
		out.WriteString(openTag)
		out.WriteString(htmlEscapeExceptTags(inner))
		out.WriteString(closeTag)
		// Continue scanning from after the closing delimiter.
		s = rest[end+len(delim):]
	}
}

// renderLinks converts Markdown [label](url) to HTML <a href="url">label</a>.
// URLs are sanitized through SanitizeURL so a stray `javascript:` or
// file:// link from a poorly-escaped source cannot inject XSS into
// the chat.
func renderLinks(s string) string {
	var out strings.Builder
	for {
		lb := strings.Index(s, "[")
		if lb < 0 {
			out.WriteString(s)
			return out.String()
		}
		mid := strings.Index(s[lb:], "](")
		if mid < 0 {
			// No closing `](` — no more links.
			out.WriteString(s)
			return out.String()
		}
		// mid is relative to s[lb:]; absolute positions:
		rb := lb + mid
		// search for the closing `)` after the "]("
		after := rb + 2 // position right after "]("
		parenClose := strings.Index(s[after:], ")")
		if parenClose < 0 {
			out.WriteString(s)
			return out.String()
		}
		// Compose the candidate link. label = s[lb+1:rb],
		// url = s[after:after+parenClose].
		label := s[lb+1 : rb]
		rawURL := s[after : after+parenClose]
		// Emit everything before the link.
		out.WriteString(s[:lb])
		safe := SanitizeURL(rawURL)
		if safe == "" {
			// Rejected URL: emit just the label as escaped text.
			out.WriteString(htmlEscapeExceptTags(label))
		} else {
			// label may contain user text; escape it but keep
			// the href untouched (already validated by
			// SanitizeURL — we still need to escape `&` in
			// the href, so do it inside SanitizeURL? no — keep
			// SanitizeURL pure and escape the url here).
			out.WriteString("<a href=\"")
			out.WriteString(htmlEscapeAttr(safe))
			out.WriteString("\">")
			out.WriteString(htmlEscapeExceptTags(label))
			out.WriteString("</a>")
		}
		s = s[after+parenClose+1:]
	}
}

// renderCodeBlock emits a <pre>…</pre> block. If the content is over
// CodeBlockInlineCap, callers should switch to file delivery via
// formatCodeAsFile (this function just returns the inline form).
// unclosed is true when the source had an opening ``` but no matching
// close — we still emit a block, with a small notice.
func (f *formatter) renderCodeBlock(content, lang string, unclosed bool) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return ""
	}
	// We deliberately drop the language hint — Telegram's <pre> has
	// no syntax-highlighting channel, so the lang tag would only
	// clutter the output. Operators wanting a code view get a file
	// attachment via formatCodeAsFile.
	if unclosed {
		content += "\n…(unterminated code block)"
	}
	return "<pre>" + htmlEscapeExceptTags(content) + "</pre>"
}

// renderCodeBlockAsFile returns the byte payload + filename + caption
// for a `sendDocument` call when the code block is too long for an
// inline <pre>. The extension defaults to `.txt` unless `lang` looks
// like a real filename hint (e.g. "json", "python").
func (f *formatter) renderCodeBlockAsFile(content, lang string) (filename string, body []byte) {
	ext := ".txt"
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "json":
		ext = ".json"
	case "js", "javascript":
		ext = ".js"
	case "py", "python":
		ext = ".py"
	case "go", "golang":
		ext = ".go"
	case "sh", "bash", "shell":
		ext = ".sh"
	case "html":
		ext = ".html"
	case "css":
		ext = ".css"
	case "yaml", "yml":
		ext = ".yaml"
	case "md", "markdown":
		ext = ".md"
	}
	return "onyx_result" + ext, []byte(content)
}

// formatCodeAsFile is the public version: it returns the bytes for
// the file payload and a short caption (<= MaxCaptionChars) to be
// used with sendDocument.
func (f *formatter) formatCodeAsFile(content, lang, summary string) (filename string, body []byte, caption string) {
	filename, body = f.renderCodeBlockAsFile(content, lang)
	caption = strings.TrimSpace(summary)
	if caption == "" {
		caption = "code block"
	}
	if len(caption) > MaxCaptionChars {
		caption = caption[:MaxCaptionChars-1] + "…"
	}
	return filename, body, caption
}

// formatCitations converts a `[]Citation` into an HTML "Sources"
// block. We use <a href> for each link so users get clickable
// citations in the chat client. Sources are deduplicated by URL to
// keep the message short.
func (f *formatter) formatCitations(sources []Citation) string {
	if len(sources) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(sources))
	var lines []string
	for _, s := range sources {
		clean := SanitizeURL(s.URL)
		if clean == "" {
			continue
		}
		if _, dup := seen[clean]; dup {
			continue
		}
		seen[clean] = struct{}{}
		title := strings.TrimSpace(s.Title)
		if title == "" {
			title = clean
		}
		// Truncate long titles so the citations block stays
		// under the chunker limit. We keep the link intact.
		if len(title) > 120 {
			title = title[:119] + "…"
		}
		lines = append(lines, fmt.Sprintf("\u2022 <a href=\"%s\">%s</a>", htmlEscapeAttr(clean), htmlEscapeExceptTags(title)))
	}
	if len(lines) == 0 {
		return ""
	}
	return "<b>Sources</b>\n" + strings.Join(lines, "\n")
}

// renderMarkdownReport is the top-level entry point: take report_md +
// sources, return the HTML body and a flag indicating whether the
// caller should prefer a file attachment (i.e. the result is huge).
// The function deliberately does NOT chunk — that's the caller's job,
// so we can re-chunk at the call site with a smaller cap that absorbs
// the trailing-citation block.
func (f *formatter) renderMarkdownReport(reportMD string, sources []Citation) (html string, useFile bool) {
	body := f.MarkdownToHTML(reportMD)
	if citations := f.formatCitations(sources); citations != "" {
		if body != "" {
			body += "\n\n" + citations
		} else {
			body = citations
		}
	}
	// Decide file vs chunked-message delivery based on size. The
	// chunker is fine for any body under FileFallbackThreshold; above
	// that, the chat becomes a wall of text and a .md attachment is
	// far more usable.
	if len(body) > FileFallbackThreshold {
		return body, true
	}
	return body, false
}

// formatJSONResult is the convenience used by /extract. JSON is
// pretty-printed with 2-space indent (cheap to inline) and either
// returned as a JSON object (len < CodeBlockInlineCap) or routed to
// a file attachment with a short caption.
func (f *formatter) formatJSONResult(v interface{}) (html string, useFile bool, filename string, body []byte, caption string) {
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Should not happen for the typed values /extract returns
		// (map[string]any) but we degrade gracefully.
		pretty = []byte(fmt.Sprintf("%v", v))
	}
	s := string(pretty)
	if len(s) <= CodeBlockInlineCap {
		return "<pre>" + htmlEscapeExceptTags(s) + "</pre>", false, "", nil, ""
	}
	filename, body, caption = f.formatCodeAsFile(s, "json", "extraction result (JSON)")
	return "", true, filename, body, caption
}

// ----- small HTML escape helpers -----

// htmlEscapeExceptTags escapes `&`, `<`, `>` and `"` UNLESS the
// string already contains a `<tag>` we trust. We trust the tags we
// emit ourselves (`<b>`, `<i>`, `<pre>`, `<code>`, `<a href="...">`
// + their `</…>` closers). Any other `<…>` is escaped to `&lt;…&gt;`
// so user text containing literal angle brackets cannot inject HTML
// into the chat.
func htmlEscapeExceptTags(s string) string {
	if s == "" {
		return ""
	}
	// longestAllowedTagAt returns the byte length of the longest
	// allowed tag at the start of `rest`, or 0 if `rest` does not
	// begin with an allowed tag. We need a custom matcher (rather
	// than HasPrefix chains) because `<a href="…">` has variable
	// length and we must consume through the closing `>` of the
	// open tag only — the inner content is NOT consumed here, it
	// will be visited by the outer loop on subsequent iterations.
	matchAt := func(rest string) int {
		// Order matters: longer/more specific prefixes first, so
		// we don't accidentally match the prefix of a longer tag.
		switch {
		case strings.HasPrefix(rest, "<b>"):
			return 3
		case strings.HasPrefix(rest, "</b>"):
			return 4
		case strings.HasPrefix(rest, "<i>"):
			return 3
		case strings.HasPrefix(rest, "</i>"):
			return 4
		case strings.HasPrefix(rest, "<pre>"):
			return 5
		case strings.HasPrefix(rest, "</pre>"):
			return 6
		case strings.HasPrefix(rest, "<code>"):
			return 6
		case strings.HasPrefix(rest, "</code>"):
			return 7
		case strings.HasPrefix(rest, "<a href=\""):
			// We have the prefix "<a href=\"…\""; now we need
			// to find the matching `">` that closes the open
			// tag. Bail out (return 0) if not found, so the
			// outer loop escapes the `<` as text.
			after := len("<a href=\"")
			closeQuote := strings.Index(rest[after:], "\">")
			if closeQuote < 0 {
				return 0
			}
			return after + closeQuote + 2
		case strings.HasPrefix(rest, "</a>"):
			return 4
		}
		return 0
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		ch := s[i]
		switch ch {
		case '&':
			out.WriteString("&amp;")
			i++
		case '<':
			if skip := matchAt(s[i:]); skip > 0 {
				out.WriteString(s[i : i+skip])
				i += skip
				continue
			}
			// Disallowed: escape.
			out.WriteString("&lt;")
			i++
		case '>':
			// Always escape: Telegram tolerates `&gt;` in any
			// text position, and ">" by itself is rare enough
			// in chat content that the visual cost is small.
			out.WriteString("&gt;")
			i++
		case '"':
			out.WriteString("&quot;")
			i++
		default:
			out.WriteByte(ch)
			i++
		}
	}
	return out.String()
}

// htmlEscapeAttr escapes for use inside an HTML attribute value.
// Stricter than the body escape: we keep `"` escaped to `&quot;` so
// the attribute can't be closed prematurely by user input.
func htmlEscapeAttr(s string) string {
	r := strings.NewReplacer(
		`&`, `&amp;`,
		`<`, `&lt;`,
		`>`, `&gt;`,
		`"`, `&quot;`,
	)
	return r.Replace(s)
}

// Citation is the minimal source reference the formatter needs. It
// is intentionally decoupled from the agent/findings structs so this
// package does not import the research package.
type Citation struct {
	URL   string
	Title string
	// Confidence is optional (0..1). Used by renderMarkdownReport to
	// sort / drop low-confidence citations when too many are
	// available; currently informational only.
	Confidence float64
}
