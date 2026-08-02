package telegram

import (
	"strings"
	"testing"
)

func TestFormatter_HTMLEscapeExceptTags_AllowsOurTags(t *testing.T) {
	in := "hello <b>bold</b> and <i>italic</i>"
	got := htmlEscapeExceptTags(in)
	if !strings.Contains(got, "<b>bold</b>") {
		t.Errorf("expected <b>bold</b> preserved, got %q", got)
	}
	if !strings.Contains(got, "<i>italic</i>") {
		t.Errorf("expected <i>italic</i> preserved, got %q", got)
	}
}

func TestFormatter_HTMLEscapeExceptTags_EscapesUserTags(t *testing.T) {
	// User text with a literal <script> tag must NOT make it through
	// as a tag.
	in := "click <script>alert(1)</script> now"
	got := htmlEscapeExceptTags(in)
	if strings.Contains(got, "<script>") {
		t.Errorf("expected <script> to be escaped, got %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped form, got %q", got)
	}
}

func TestFormatter_BoldAndItalic(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("**bold** and __italic__ and **also bold**")
	if !strings.Contains(got, "<b>bold</b>") {
		t.Errorf("**bold** did not render; got %q", got)
	}
	if !strings.Contains(got, "<i>italic</i>") {
		t.Errorf("__italic__ did not render; got %q", got)
	}
	if !strings.Contains(got, "<b>also bold</b>") {
		t.Errorf("**also bold** did not render; got %q", got)
	}
}

func TestFormatter_Header(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("# Title\n\nbody")
	if !strings.Contains(got, "<b>Title</b>") {
		t.Errorf("# header did not render; got %q", got)
	}
}

func TestFormatter_Blockquote(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("> a quoted line")
	if !strings.Contains(got, "<i>a quoted line</i>") {
		t.Errorf("> blockquote did not render; got %q", got)
	}
}

func TestFormatter_BulletedList(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("- one\n- two")
	if !strings.Contains(got, "\u2022\u00a0one") {
		t.Errorf("- one did not render as bullet; got %q", got)
	}
	if !strings.Contains(got, "\u2022\u00a0two") {
		t.Errorf("- two did not render as bullet; got %q", got)
	}
}

func TestFormatter_InlineLink(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("see [the doc](https://example.com/path)")
	if !strings.Contains(got, `<a href="https://example.com/path">the doc</a>`) {
		t.Errorf("link did not render; got %q", got)
	}
}

func TestFormatter_InlineLink_RejectsUnsafeURL(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("see [bad](javascript:alert(1))")
	if strings.Contains(got, "javascript:") {
		t.Errorf("javascript: link must be rejected; got %q", got)
	}
	// The label should still be present (as escaped text).
	if !strings.Contains(got, "bad") {
		t.Errorf("label should be preserved as plain text; got %q", got)
	}
}

func TestFormatter_CodeBlock_RendersAsPre(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("```\nplain text\n```")
	if !strings.Contains(got, "<pre>") {
		t.Errorf("code block did not render as <pre>; got %q", got)
	}
	if !strings.Contains(got, "plain text") {
		t.Errorf("code block content lost; got %q", got)
	}
}

func TestFormatter_CodeBlock_EscapesHTML(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("```\n<script>alert(1)</script>\n```")
	if strings.Contains(got, "<script>") {
		t.Errorf("<script> must be escaped inside <pre>; got %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped <script> inside code block; got %q", got)
	}
}

func TestFormatter_UnclosedCodeBlock_StillRenders(t *testing.T) {
	f := newFormatter()
	got := f.MarkdownToHTML("```\nhalf a block")
	if !strings.Contains(got, "<pre>") {
		t.Errorf("unclosed code block must still render; got %q", got)
	}
	if !strings.Contains(got, "unterminated") {
		t.Errorf("unclosed code block should add a notice; got %q", got)
	}
}

func TestFormatter_Citations_AppendedAfterBody(t *testing.T) {
	f := newFormatter()
	body, useFile := f.renderMarkdownReport("the report", []Citation{
		{URL: "https://example.com/a", Title: "A"},
		{URL: "https://example.com/b", Title: "B"},
	})
	if useFile {
		t.Errorf("short report should not need file fallback")
	}
	if !strings.Contains(body, "the report") {
		t.Errorf("body missing in output: %q", body)
	}
	if !strings.Contains(body, "Sources") {
		t.Errorf("citations header missing: %q", body)
	}
	if !strings.Contains(body, `<a href="https://example.com/a">A</a>`) {
		t.Errorf("link A missing: %q", body)
	}
	if !strings.Contains(body, `<a href="https://example.com/b">B</a>`) {
		t.Errorf("link B missing: %q", body)
	}
}

func TestFormatter_Citations_DedupedByURL(t *testing.T) {
	f := newFormatter()
	body, _ := f.renderMarkdownReport("body", []Citation{
		{URL: "https://example.com/a", Title: "A1"},
		{URL: "https://example.com/a", Title: "A2"},
		{URL: "https://example.com/b", Title: "B"},
	})
	// Count occurrences of the dedup'd URL.
	if got := strings.Count(body, `href="https://example.com/a"`); got != 1 {
		t.Errorf("duplicate URL not deduped; got %d occurrences in %q", got, body)
	}
}

func TestFormatter_Citations_RejectNonHTTP(t *testing.T) {
	f := newFormatter()
	body, _ := f.renderMarkdownReport("body", []Citation{
		{URL: "javascript:alert(1)", Title: "BAD"},
		{URL: "file:///etc/passwd", Title: "ALSO BAD"},
		{URL: "https://example.com/ok", Title: "OK"},
	})
	if strings.Contains(body, "javascript:") {
		t.Errorf("javascript: citation must be rejected; got %q", body)
	}
	if strings.Contains(body, "file://") {
		t.Errorf("file:// citation must be rejected; got %q", body)
	}
	if !strings.Contains(body, "https://example.com/ok") {
		t.Errorf("https citation must be kept; got %q", body)
	}
}

func TestFormatter_FileFallbackTrigger(t *testing.T) {
	f := newFormatter()
	huge := strings.Repeat("a paragraph of text. ", FileFallbackThreshold/20)
	body, useFile := f.renderMarkdownReport(huge, nil)
	if !useFile {
		t.Errorf("expected file fallback for %d-char body, got inline", len(body))
	}
}

func TestFormatter_InlineJSONResult_Short(t *testing.T) {
	f := newFormatter()
	html, useFile, _, _, _ := f.formatJSONResult(map[string]any{"a": 1})
	if useFile {
		t.Errorf("small JSON should be inline, not file")
	}
	if !strings.Contains(html, "<pre>") {
		t.Errorf("inline JSON should use <pre>; got %q", html)
	}
}

func TestFormatter_InlineJSONResult_LargeRoutesToFile(t *testing.T) {
	f := newFormatter()
	big := map[string]any{}
	for i := 0; i < 200; i++ {
		big["key"+string(rune('a'+i%26))+"_with_a_longish_suffix"] = strings.Repeat("value ", 10)
	}
	html, useFile, filename, body, _ := f.formatJSONResult(big)
	if useFile != true {
		t.Errorf("large JSON should trigger file fallback, got inline; html=%q", html)
	}
	if filename == "" {
		t.Errorf("filename must be set in file fallback")
	}
	if len(body) == 0 {
		t.Errorf("file body must be non-empty")
	}
	if !strings.HasSuffix(filename, ".json") {
		t.Errorf("JSON file should have .json extension; got %q", filename)
	}
}

func TestFormatter_RenderCodeBlockAsFile_PicksExtensionFromLang(t *testing.T) {
	f := newFormatter()
	cases := []struct{ lang, ext string }{
		{"json", ".json"},
		{"JSON", ".json"},
		{"python", ".py"},
		{"go", ".go"},
		{"javascript", ".js"},
		{"unknown", ".txt"},
	}
	for _, c := range cases {
		fn, body := f.renderCodeBlockAsFile("payload", c.lang)
		if !strings.HasSuffix(fn, c.ext) {
			t.Errorf("lang=%q: expected ext %q, got %q", c.lang, c.ext, fn)
		}
		if string(body) != "payload" {
			t.Errorf("lang=%q: body mismatch; got %q", c.lang, body)
		}
	}
}

func TestFormatter_RenderLine_DropsEmptyHeader(t *testing.T) {
	f := newFormatter()
	if got := f.renderLine("#"); got != "" {
		t.Errorf("empty header should render as empty, got %q", got)
	}
	if got := f.renderLine("# "); got != "" {
		t.Errorf("whitespace-only header should render as empty, got %q", got)
	}
}

func TestFormatter_RenderLine_HandlesMultipleHashes(t *testing.T) {
	f := newFormatter()
	// "## Section" should render as bold, regardless of how many hashes.
	got := f.renderLine("### Deep")
	if !strings.Contains(got, "<b>Deep</b>") {
		t.Errorf("### header did not render; got %q", got)
	}
}

func TestFormatter_HTMLEscapeExceptTags_HandlesEmptyInput(t *testing.T) {
	if got := htmlEscapeExceptTags(""); got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
}

func TestFormatter_HTMLEscapeExceptTags_EscapesAmpersand(t *testing.T) {
	if got := htmlEscapeExceptTags("A & B"); got != "A &amp; B" {
		t.Errorf("ampersand not escaped; got %q", got)
	}
}

func TestFormatter_HTMLEscapeAttr_EscapesAllFour(t *testing.T) {
	in := `a&b<c>d"e`
	want := `a&amp;b&lt;c&gt;d&quot;e`
	if got := htmlEscapeAttr(in); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
