package news

import (
	"fmt"
	"regexp"
	"strings"
)

// FormatMarkdown renders a NewsDigest as a single Markdown report
// string shaped the same way as the agent's "Final Report":
//
//	# News Digest — window: …
//
//	## <field name>
//
//	### <article title>
//
//	*<source · relative time · integrity>*
//
//	<overview paragraph — the LLM-generated or fallback takeaway>
//
//	<cleaned article body, rendered as readable paragraphs>
//
//	[Read original →](<url>)
//
// The Web UI feeds this string through `marked.parse()` to get the
// same styled report card the agent uses, instead of dumping raw
// publisher HTML into a custom layout. Each field becomes one
// "## " sub-section under a top-level "# " digest header so the
// per-field separation required by Phase 9 is preserved while the
// per-article shape matches the agent's report.
func FormatMarkdown(digest *NewsDigest) string {
	return FormatMarkdownView(BuildDigestView(digest))
}

// FormatMarkdownView is the render-ready counterpart of
// FormatMarkdown, accepting a DigestView so renderers can reuse
// the same view object across surfaces (HTML, Markdown, CLI,
// Telegram).
func FormatMarkdownView(view *DigestView) string {
	if view == nil {
		return "*No news digest generated.*\n"
	}
	if len(view.Fields) == 0 {
		return "*No profile fields configured or processed.*\n"
	}

	var sb strings.Builder

	// Top-level digest header — H1.
	window := strings.TrimSpace(view.Window)
	if window == "" {
		window = "recent"
	}
	fmt.Fprintf(&sb, "# News Digest — window: %s\n\n", window)
	fmt.Fprintf(&sb, "*run #%d · %d field(s) · %d item(s)*\n\n",
		view.RunID,
		len(view.Fields),
		totalItemCount(view),
	)

	for fi, fv := range view.Fields {
		if fi > 0 {
			sb.WriteString("\n---\n\n")
		}

		// Field section header — H2, mirrors the agent's "Core
		// Technical Benefits"-style sub-section pattern.
		fmt.Fprintf(&sb, "## %s\n\n", strings.TrimSpace(fv.FieldName))

		if fv.ItemCount == 0 {
			sb.WriteString("*No articles found for this field.*\n")
			continue
		}

		for ii, item := range fv.Items {
			if ii > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(formatMarkdownItem(ii, item))
		}
	}

	return sb.String()
}

// formatMarkdownItem renders a single article in the new digest shape:
//
//	#1CIDRAP
//
//	Artificial intelligence could usher in a new era of vaccine development
//
//	Artificial intelligence could revolutionize vaccine development by...
//
// No meta line, no [Read original →], no ### heading. The body is the
// LLM-short 2-3 sentence summary already stored in item.Body.
func formatMarkdownItem(idx int, item ItemView) string {
	var sb strings.Builder

	// #N + SOURCE chip (no space between # and source name per screenshot).
	source := strings.ToUpper(strings.TrimSpace(item.Source))
	if source == "" {
		source = "UNKNOWN"
	}
	fmt.Fprintf(&sb, "#%d%s\n\n", idx+1, source)

	// Headline.
	title := strings.TrimSpace(item.Headline)
	if title == "" {
		title = item.URL
	}
	fmt.Fprintf(&sb, "%s\n\n", title)

	// Body — the LLM-short 2-3 sentence summary. Already short,
	// no need for the cleanBodyForMarkdownReport pass.
	body := strings.TrimSpace(item.Body)
	if body != "" {
		fmt.Fprintf(&sb, "%s\n\n", body)
	}

	return sb.String()
}


// buildMarkdownMeta composes the italicised meta line.
func buildMarkdownMeta(source, relativeTime, fetchIntegrity, confidenceFlag string) string {
	parts := []string{}
	if s := strings.TrimSpace(source); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(relativeTime); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(fetchIntegrity); s != "" {
		if strings.EqualFold(s, "ok") || strings.EqualFold(s, "high") {
			parts = append(parts, "verified")
		} else {
			parts = append(parts, "integrity: "+s)
		}
	}
	if s := strings.TrimSpace(confidenceFlag); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

func totalItemsInDigest(d *NewsDigest) int {
	n := 0
	for _, f := range d.Fields {
		n += len(f.Items)
	}
	return n
}

// Body-cleaning rules for the Markdown report.
//
// Publisher full-text pulls are noisy: "Skip to content" anchors,
// "Sign up for email newsletters" CTAs, "Most Popular" list
// previews, "Featured" stubs, list items collapsed to single
// dashes, and a flood of inline markdown links to other articles
// on the same site. We strip those patterns so the rendered
// report is readable as an actual article, not a navigation
// dump.
var (
	// Lines that are ONLY a markdown link — typically the
	// "See also" / "Most Popular" items in a publisher sidebar.
	linkOnlyLineRE = regexp.MustCompile(`^\s*\[[^\]]*\]\([^)]*\)\s*$`)

	// Lines that are JUST a single dash / bullet marker,
	// which publishers emit to represent empty list items.
	dashOnlyLineRE = regexp.MustCompile(`^\s*[-*]\s*$`)

	// Heading whose body is a single markdown link —
	// publishers emit `##   [Sports](https://courant.com/sports/)`
	// as their site-section nav.
	headingLinkOnlyRE = regexp.MustCompile(`^\s*#{1,6}\s+\[[^\]]+\]\([^)]+\)\s*$`)

	// Common publisher navigation phrases that have no
	// informational value to the operator.
	navPhrases = []string{
		"Skip to content",
		"Skip to main content",
		"Skip to navigation",
		"Sign up for email newsletters",
		"Sign up for our newsletter",
		"Subscribe to our newsletter",
		"Most Popular",
		"Most Read",
		"Related Articles",
		"Related Stories",
		"Trending Now",
		"Newsletter Signup",
		"Latest Headlines",
		"Breaking News",
		"Featured",
		"Featured Stories",
		"Top Stories",
		"Top Headlines",
		"Editor's Picks",
		"Editors' Picks",
		"Recommended",
		"You May Also Like",
		"Read More",
		"More From",
		"Around the Web",
		"From Our Partners",
		"Continue Reading",
		"Continue reading",
		"Show full article",
		"Continue without subscribing",
		"Trending",
		"FREE FUN & GAMES",
		"PHOTOS",
		"Photos",
	}

	// Heading patterns that are just bare category labels
	// with no body content following them — emitted by
	// publisher nav systems. The body may include
	// apostrophes, commas, colons, parentheses, em-dashes
	// etc. (real headlines like `Billionaires' plans for
	// CT 'island' hits opposition` are exactly the pattern
	// we want to catch as a nav cluster).
	//
	// The character class also includes the most common
	// UTF-8-encoded punctuation that publishers emit (e.g.
	// curly apostrophe `'` / `'` / `'`, em-dash, en-dash,
	// ellipsis `…`) so non-ASCII headlines from
	// international publishers still match. Without these,
	// a real headline like `CT estate combines classic New
	// England style with panoramic water views. It's
	// listed for $6.8M` (which contains the curly
	// apostrophe) is silently ignored by the
	// cluster-detection pass and the whole related-articles
	// nav cluster leaks into the cleaned body.
	shortHeadingRE = regexp.MustCompile(`^\s*#{1,6}\s+([A-Z][A-Za-z0-9 &/.\-,:;!?'‘’""()–—…$]{1,120})\s*$`)

	// Heading whose body is `### [Some headline](url)` —
	// publishers emit these as their "You may also like"
	// list under a fake heading prefix.
	headingWithLinkInsideRE = regexp.MustCompile(`^\s*#{1,6}\s+\[[^\]]+\]\([^)]+\)\s*$`)

	// Just a heading prefix (`#`, `##`, etc.) with no
	// content — the body of the heading was probably an
	// inline-only link that the markdown parser dropped.
	emptyHeadingRE = regexp.MustCompile(`^\s*#{1,6}\s*$`)

	// Heading that contains a nav phrase anywhere (e.g.
	// `### Skip to content`).
	headingWithNavRE = regexp.MustCompile(`(?i)#{1,6}\s+(skip to|sign up|subscribe|most popular|featured|trending|related|breaking|latest|top stories|recommended|you may also|read more|continue reading)`)

	// Lines whose body is dominated by a URL — most commonly
	// the Google News tracking interstitial
	// (`https://news.google.com/rss/articles/CBMi…`) that ends
	// up in an article body when the orchestrator's full-text
	// pull failed and the RSS snippet survived. We treat a
	// line as "just a URL" if the visible text after the
	// scheme / host / path / percent-encoding decoding is
	// essentially nothing but a tiny link label. The two
	// tests together catch both the bare `https://...` line
	// and the markdown-wrapped `[label](https://...)` line
	// when the label is just a few characters.
	urlOnlySchemesRE = regexp.MustCompile(`(?i)^[\s\[]*(?:https?://|www\.)[^\s].*$`)
	encodedURLLineRE = regexp.MustCompile(`%2[fF]|%3[aAdD]|news\.google\.com/rss/articles`)

	// Short slash-delimited path token — the publisher URL
	// fragment that gets emitted on its own line after the
	// `?continue=…` percent-encoded redirect soup is
	// stripped. Shape: `engineer/remote`,
	// `jobs/role/designer`, `signup_job_id/...`. Never
	// legitimate article prose.
	shortPathOnlyRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*(?:/[a-z0-9_-]+)+$`)
)

// cleanBodyForMarkdownReport strips publisher navigation cruft
// from a raw body and returns the cleaned Markdown-friendly
// text. The rules are intentionally conservative — they only
// remove patterns that are clearly navigation/junk, not editorial
// content. The output is a single Markdown string with blank-line
// separated paragraphs.
func cleanBodyForMarkdownReport(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// Normalize line endings.
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	// Build a set for fast nav-phrase lookup (lowercased).
	navSet := make(map[string]bool, len(navPhrases))
	for _, p := range navPhrases {
		navSet[strings.ToLower(p)] = true
	}

	lines := strings.Split(normalized, "\n")

	// Pre-pass: find runs of 3+ short `###` headings in a
	// row, but ONLY in the tail of the body. Publishers put
	// their "you may also like" / "related stories" /
	// "you may be interested in" cluster at the very end of
	// the page; legitimate per-section subheadings and job
	// listings appear interspersed with the article body
	// and never at the very tail without intervening
	// paragraphs. We compute the "first-body-paragraph
	// cutoff" (the line index where the first real
	// paragraph of >= 30 words appears) and only apply
	// cluster detection to lines after that cutoff. This
	// preserves mid-body job listings on sites like
	// weworkremotely.com and indeed.com while still
	// dropping the publisher's tail nav cluster.
	dropSet := make(map[int]bool, len(lines))
	{
		// Find the line index of the first real paragraph.
		// A "real" body paragraph is a non-heading, non-link
		// line with at least 12 words. The previous threshold
		// of 30 words was too aggressive for typical
		// news articles (which open with 15-20 word
		// paragraphs and let the body grow) and caused the
		// publisher's mid-body "related stories" cluster
		// (which appears BEFORE the first long paragraph)
		// to be treated as "before the body" and skipped
		// over. With 12 words the cluster detector sees the
		// openers and can correctly classify the cluster as
		// a sidebar.
		firstBodyIdx := len(lines)
		for k, raw := range lines {
			jt := strings.TrimSpace(raw)
			if shortHeadingRE.MatchString(jt) || dashOnlyLineRE.MatchString(jt) || linkOnlyLineRE.MatchString(jt) {
				continue
			}
			wordCount := len(strings.Fields(jt))
			if wordCount >= 12 {
				firstBodyIdx = k
				break
			}
		}
		// Walk for clusters only in the tail (after the
		// first body paragraph) AND where the cluster is
		// near the end of the document.
		i := firstBodyIdx
		for i < len(lines) {
			jt := strings.TrimSpace(lines[i])
			if !shortHeadingRE.MatchString(jt) {
				i++
				continue
			}
			// Start of a possible cluster. Walk forward,
			// counting consecutive short headings (allowing
			// blank lines in between).
			j := i + 1
			clusterStart := i
			for j < len(lines) {
				jt2 := strings.TrimSpace(lines[j])
				if jt2 == "" {
					j++
					continue
				}
				if shortHeadingRE.MatchString(jt2) {
					j++
					continue
				}
				break
			}
			clusterEnd := j // exclusive
			clusterLen := 0
			for k := clusterStart; k < clusterEnd; k++ {
				if shortHeadingRE.MatchString(strings.TrimSpace(lines[k])) {
					clusterLen++
				}
			}
			// Treat as a nav-stub cluster when EITHER:
			//   (a) the cluster has 3+ headings and lives
			//       in the final 40% of the document
			//       (publisher's "you may also like" tail
			//       list), OR
			//   (b) the cluster has 5+ headings and lives
			//       anywhere AFTER the first body
			//       paragraph. This is the publisher's
			//       "you may also like" / "trending
			//       stories" / "more from this site"
			//       sidebar that's emitted mid-page on
			//       homepages and aggregator pages — the
			//       WeWorkRemotely carve-out is preserved
			//       because individual job rows there are
			//       never 5 consecutive `###` headings
			//       without intervening prose.
			if (clusterLen >= 3 && clusterStart >= int(float64(len(lines))*0.6)) ||
				(clusterLen >= 5 && clusterStart > firstBodyIdx) {
				for k := clusterStart; k < clusterEnd; k++ {
					dropSet[k] = true
				}
			}
			i = j
		}
	}

	var out []string

	for i := 0; i < len(lines); i++ {
		if dropSet[i] {
			continue
		}
		line := lines[i]

		trimmed := strings.TrimSpace(line)

		// Drop pure-dash / pure-bullet lines.
		if dashOnlyLineRE.MatchString(trimmed) {
			continue
		}

		// Drop lines that are ONLY a markdown link.
		if linkOnlyLineRE.MatchString(trimmed) {
			continue
		}

		// Drop headings that are ONLY a markdown link, e.g.
		// `##   [Sports](https://courant.com/sports/)`. These
		// are the publisher's site-section nav headings.
		if headingLinkOnlyRE.MatchString(trimmed) {
			continue
		}

		// Drop headings whose body is `### [Headline](url)` —
		// publishers emit these as their "you may also like"
		// list with a fake heading prefix.
		if headingWithLinkInsideRE.MatchString(trimmed) {
			continue
		}

		// Drop headings with no body at all (e.g. just `##`).
		if emptyHeadingRE.MatchString(trimmed) {
			continue
		}

		// Drop headings whose text contains a known nav
		// phrase (case-insensitive), e.g. `### Skip to content`
		// or `## Photos`.
		if headingWithNavRE.MatchString(trimmed) {
			continue
		}

		// Drop lines that are just a URL, or a markdown
		// link wrapping a long URL. These are almost always
		// the Google News tracking interstitial
		// (`https://news.google.com/rss/articles/CBMi…`) that
		// ends up in the body when the full-text pull failed
		// and the RSS snippet survived. The previous "link
		// only line" rule (`linkOnlyLineRE`) catches plain
		// `[label](url)` lines, but the URL can also appear
		// bare, or repeated across many paragraphs, or wrapped
		// in a "Learn more: https://…" sentence fragment.
		// We add a cheap second pass here: any line whose
		// content is dominated by URL syntax (a long encoded
		// URL, a `news.google.com/rss/articles` reference, or
		// a bare `https://…` line) is dropped outright.
		//
		// Note: looksLikeEncodedURLLine runs unconditionally
		// (not gated on urlOnlySchemesRE) because the encoded
		// URL is sometimes preceded by a publisher's own
		// label, e.g. `continue=https%3A%2F%2F…`.
		if looksLikeEncodedURLLine(trimmed) {
			continue
		}
		// A short slash-delimited path token
		// (`engineer/remote`, `jobs/role/designer`) is the
		// publisher URL fragment that Google News leaks onto
		// its own line after the percent-encoded redirect
		// soup. It is never legitimate article prose.
		if len(trimmed) <= 60 && shortPathOnlyRE.MatchString(strings.ToLower(trimmed)) {
			continue
		}
		if urlOnlySchemesRE.MatchString(trimmed) {
			// A bare `https://…` line (with no surrounding
			// prose) is always treated as URL-only.
			if isBareURLLine(trimmed) {
				continue
			}
		}

		// Drop lines that are just a known nav phrase
		// (case-insensitive). Also drop lines that START with
		// a nav phrase — publishers sometimes concatenate nav
		// anchors onto the same line as the start of a
		// section, e.g. `Skip to content ## Breaking News`.
		// When the line is otherwise short (<120 chars) and
		// starts with a nav phrase, treat the whole line as
		// nav and drop it.
		lower := strings.ToLower(trimmed)
		if navSet[lower] {
			continue
		}
		if len(trimmed) < 120 {
			for phrase := range navSet {
				if strings.HasPrefix(lower, phrase) {
					goto navPrefixMatch
				}
			}
		}
		goto navChecked
	navPrefixMatch:
		continue
	navChecked:

		// If the line is a short heading whose text is a
		// nav phrase, drop it outright. Headings like
		// "## Featured", "## Most Popular", "## Latest
		// Headlines" are always publisher nav chrome — the
		// body lines under them are a list of link stubs
		// (which the link-only / dash-only rules already drop)
		// rather than the article body.
		if m := shortHeadingRE.FindStringSubmatch(trimmed); m != nil {
			heading := strings.TrimSpace(m[1])
			if navSet[strings.ToLower(heading)] {
				continue
			}
		}

		// If the line is a short heading (any text) with no
		// real content after it (next non-blank line is also
		// a list of dash-only stubs or another heading), drop
		// the heading. This catches "## Featured\n-\n-\n-".
		if shortHeadingRE.MatchString(trimmed) {
			// Look ahead — if the next non-blank lines are
			// all dashes or links, drop the heading.
			if looksLikeEmptyHeadingAt(lines, i) {
				continue
			}
		}

		out = append(out, line)
	}

	// Collapse runs of blank lines into a single blank line.
	result := strings.Join(out, "\n")
	result = collapseBlankLines(result)

	return strings.TrimSpace(result)
}

// looksLikeEmptyHeadingAt reports whether the heading at
// lines[i] is followed by nothing meaningful — only blank
// lines, dash-only stubs, or other short headings.
func looksLikeEmptyHeadingAt(lines []string, i int) bool {
	blankOrJunk := 0
	total := 0
	for j := i + 1; j < len(lines) && j < i+6; j++ {
		t := strings.TrimSpace(lines[j])
		if t == "" {
			blankOrJunk++
			continue
		}
		total++
		if dashOnlyLineRE.MatchString(t) || linkOnlyLineRE.MatchString(t) {
			blankOrJunk++
			continue
		}
		if shortHeadingRE.MatchString(t) {
			blankOrJunk++
			continue
		}
		// Real content encountered.
		return false
	}
	// If we ran out of lines or saw only junk after the
	// heading, treat the heading as empty.
	return total == 0 || blankOrJunk == total
}

var blankLineRunRE = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string {
	return blankLineRunRE.ReplaceAllString(s, "\n\n")
}

// looksLikeEncodedURLLine reports whether the given line is
// dominated by URL-encoded or repeated URL syntax. It is the
// safety net for the orchestrator's "the full-text pull
// failed and the RSS snippet survived" failure mode: the
// resulting body is full of lines like
//
//	https://news.google.com/rss/articles/CBMi…?continue=https%3A%2F%2F…
//
// which the rest of the cleaner happily passes through as
// "real prose". The two cheap tests:
//
//  1. The line is mostly percent-encoded characters
//     (`%2F`, `%3A`, `%3D`, …) — a strong signal that the
//     line is a Google News tracking URL.
//  2. The line contains a `news.google.com/rss/articles`
//     reference — the exact path the Google News RSS
//     interstitial uses.
//
// We keep the test conservative: a real article line like
// "She cited a 50% rise in revenue" has one `%` and no
// `news.google.com`, so neither test fires.
func looksLikeEncodedURLLine(line string) bool {
	if line == "" {
		return false
	}
	// 1. Heavy percent-encoding.
	percentCount := strings.Count(line, "%")
	// A normal URL has 1-2 `%` escapes (e.g. "%20" for a
	// space). A Google News tracking URL has 15+ in a row
	// (one per encoded char in the embedded `?continue=…`
	// parameter). Threshold at 5 to be safe.
	if percentCount >= 5 {
		return true
	}
	// 2. The Google News RSS path is a hard signal.
	if encodedURLLineRE.MatchString(line) {
		return true
	}
	return false
}

// isBareURLLine reports whether the line is essentially just
// one URL with no surrounding prose. We catch two shapes:
// the bare `https://…` line and the markdown
// `[label](https://…)` line where the label is a single
// short word and the link dominates the line length.
func isBareURLLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	// Strip an optional leading "[label](url)" markdown wrap.
	stripped := strippedMarkdownLink(trimmed)
	if stripped == "" {
		// The whole line was `[…](…)` — already handled by
		// linkOnlyLineRE, but treat as URL-only anyway so the
		// caller's logic stays linear.
		return true
	}
	// What remains after the markdown wrap should itself be a
	// URL (possibly followed by tiny trailing text like
	// "Source"). The same `urlOnlySchemesRE` test from the
	// caller is reused, but applied to the remainder.
	return urlOnlySchemesRE.MatchString(strings.TrimSpace(stripped))
}

// strippedMarkdownLink returns the text after stripping a
// leading `[label](url)` markdown link. Returns the original
// string when no link is present, or "" when the whole line
// was a single link.
var leadingMarkdownLinkRE = regexp.MustCompile(`^\s*\[[^\]]+\]\([^)]+\)\s*`)

func strippedMarkdownLink(line string) string {
	if leadingMarkdownLinkRE.MatchString(line) {
		rest := leadingMarkdownLinkRE.ReplaceAllString(line, "")
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return ""
		}
		return rest
	}
	return line
}

// inlineMDLinkRE matches inline markdown links [text](url) anywhere in a line.
var inlineMDLinkRE = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// mdHeadingPrefixRE matches markdown heading markers at line start.
var mdHeadingPrefixRE = regexp.MustCompile(`(?m)^#{1,6}\s*`)

// proseNavPrefixesLC is the lowercased list of paragraph-start patterns
// that identify publisher navigation / boilerplate / marketing copy.
// Checked against the first 80 chars of a cleaned paragraph.
var proseNavPrefixesLC = []string{
	"skip to", "accessibility", "subscribe", "sign in", "sign up",
	"log in", "log out", "menu ", "search ", "cookie",
	"advertisement", "sponsored", "view company", "view profile",
	"boosted", "refine job", "upload", "get notified", "not sure where",
	"read today", "e-edition", "breaking news", "featured",
	"latest headlines", "most popular", "most read",
	"free fun", "jumble", "crossword", "sudoku", "solitaire",
	"trusted by", "work from home", "home attributions",
	"privacy policy", "contact us", "copyright ©",
	"declaration of independence", "this day @ law",
	"s&p 500", "stock advisor", "motley fool", "these are the stocks",
	"cumulative growth", "investing solutions", "don't miss out",
	"get started now", "sign up today", "learn more about",
	"our purpose", "our top personal finance",
	"×", "▲", "▼",
	"remote customer support", "full-stack programming jobs",
	"remote software engineer", "work from home",
	"flexible full stack", "2000+", "latest post about",
	// Motley Fool boilerplate
	"the motley fool offers", "the motley fool helps",
	"about the motley fool", "the motley fool investing",
	"founded in 1993",
	// Medical citation lists (Cureus, PubMed, etc.)
	"case report ", "original article ", "review article ",
	"systematic review ", "meta-analysis ", "clinical trial ",
	"see all",
	// Sidebar clickbait openers
	"ever feel", "have you ever", "here's why", "did you know",
	// Motley Fool product listings / nav
	"top stocks", "high-yield dividend", "best stocks",
	// Publisher quiz / interactive feature promos
	"our entire collection", "our collection of",
}

// extractProseBody extracts a clean 2-sentence prose summary from raw
// crawled text using a paragraph-first strategy:
//
//  1. Strip inline markdown links and heading markers.
//  2. Split on blank lines (paragraph breaks) — preserves article structure.
//  3. For each paragraph: clean, collapse whitespace, check nav prefixes.
//  4. A qualifying paragraph must yield ≥ 2 sentences each ≥ 55 chars.
//  5. Return the first 2 sentences from the first qualifying paragraph.
//
// If no paragraph qualifies, returns "". Silence is better than
// publishing sidebar headlines or marketing copy as an article summary.
func extractProseBody(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	// 1. Strip inline markdown links: [label](url) → label or " ".
	cleaned := inlineMDLinkRE.ReplaceAllStringFunc(raw, func(m string) string {
		sub := inlineMDLinkRE.FindStringSubmatch(m)
		if len(sub) > 1 {
			label := strings.TrimSpace(sub[1])
			if len(label) > 3 {
				return label
			}
		}
		return " "
	})

	// 2. Strip heading-marker prefixes (### → "").
	cleaned = mdHeadingPrefixRE.ReplaceAllString(cleaned, "")

	// 3. Normalise line endings.
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")

	// 4. Split into paragraphs on blank lines.
	paragraphs := strings.Split(cleaned, "\n\n")

	for _, para := range paragraphs {
		// Collapse single newlines within the paragraph into spaces —
		// each paragraph is now a single flat string.
		para = strings.Join(strings.Fields(strings.ReplaceAll(para, "\n", " ")), " ")

		// Skip trivially short paragraphs. 160 chars is enough to
		// reject single clickbait sidebar snippets (which are typically
		// 1-2 short sentences < 160 chars total) while passing real
		// article paragraphs.
		if len(para) < 160 {
			continue
		}

		// Skip paragraphs that start with known nav / boilerplate patterns.
		// Check the first 80 chars (lowercased) to keep it cheap.
		end := 80
		if end > len(para) {
			end = len(para)
		}
		prefix := strings.ToLower(para[:end])
		skip := false
		for _, pfx := range proseNavPrefixesLC {
			if strings.HasPrefix(prefix, pfx) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		// Skip paragraphs that still contain URL artifacts.
		if strings.Contains(para, "](http") || strings.HasPrefix(para, "http") {
			continue
		}

		// 5. Extract sentences using ". [A-Z]" as a boundary.
		// minLen=70 rejects short clickbait sentences (typically
		// 55-65 chars) while keeping real news sentences (80+).
		sentences := proseSentences(para, 70)

		// Require at least 2 qualifying sentences — this is the key
		// filter: sidebar items are single-sentence lines and won't
		// form multi-sentence paragraphs. Real article paragraphs do.
		if len(sentences) < 2 {
			continue
		}

		return sentences[0] + " " + sentences[1]
	}

	return ""
}

// proseSentences splits text into sentences at ". [A-Z]" / "! [A-Z]" /
// "? [A-Z]" boundaries and returns those with length >= minLen.
func proseSentences(text string, minLen int) []string {
	var out []string
	start := 0
	for i := 0; i < len(text)-2; i++ {
		ch := text[i]
		if ch != '.' && ch != '!' && ch != '?' {
			continue
		}
		next := text[i+1]
		after := text[i+2]
		if next == ' ' && after >= 'A' && after <= 'Z' {
			s := strings.TrimSpace(text[start : i+1])
			if len(s) >= minLen {
				out = append(out, s)
			}
			start = i + 2
		}
	}
	// Trailing fragment.
	if start < len(text) {
		s := strings.TrimSpace(text[start:])
		if len(s) >= minLen {
			out = append(out, s)
		}
	}
	return out
}
