package news

import (
	"strings"
	"testing"
)

func TestFormatMarkdown_BasicShape(t *testing.T) {
	digest := &NewsDigest{
		RunID:  42,
		Window: "past 24 hours",
		Fields: []FieldDigest{
			{
				FieldID:   1,
				FieldName: "AI/ML",
				Items: []DigestItem{
					{
						Headline: "Local LLMs cost less than you think",
						URL:      "https://example.com/article-1",
						Source:   "Example Daily",
						Takeaway: "On-device models can match hosted APIs in many tasks.",
						Body:     "On-device models have grown fast. Benchmarks show they're competitive.",
					},
				},
			},
		},
	}
	md := FormatMarkdown(digest)
	// Digest-level H1 is kept.
	if !strings.HasPrefix(md, "# News Digest — window: past 24 hours") {
		t.Errorf("missing top-level H1 digest header, got:\n%s", md)
	}
	// Field-level H2 is kept.
	if !strings.Contains(md, "## AI/ML") {
		t.Errorf("missing field H2, got:\n%s", md)
	}
	// New shape: #N + SOURCE chip (no space between # and source name).
	if !strings.Contains(md, "#1EXAMPLE DAILY") {
		t.Errorf("missing new item chip '#1EXAMPLE DAILY', got:\n%s", md)
	}
	// Headline as plain paragraph (not H3).
	if !strings.Contains(md, "Local LLMs cost less than you think") {
		t.Errorf("missing article headline, got:\n%s", md)
	}
	// Body present.
	if !strings.Contains(md, "On-device models have grown fast") {
		t.Errorf("missing body, got:\n%s", md)
	}
	// Old elements must be absent.
	if strings.Contains(md, "### Local LLMs") {
		t.Errorf("H3 heading must not appear in new format, got:\n%s", md)
	}
	if strings.Contains(md, "[Read original") {
		t.Errorf("[Read original] link must not appear in new format, got:\n%s", md)
	}
	if strings.Contains(md, "*Example Daily") {
		t.Errorf("italic meta line must not appear in new format, got:\n%s", md)
	}
}

func TestCleanBodyForMarkdownReport_StripsNavCruft(t *testing.T) {
	input := `Skip to content

## Breaking News

### Some other headline

## Featured

Luke Bronin is known for his high-profile resume.

## Latest Headlines

-
-
-
-

Sign up for email newsletters

[Most Popular](https://example.com/most-popular)

The real content of the article starts here. It has several
paragraphs of real information that the operator actually wants
to read.

The article concludes with a second paragraph.
`
	got := cleanBodyForMarkdownReport(input)

	// Must drop: "Skip to content", "## Breaking News", the linked
	// headline under it, "## Featured", "## Latest Headlines", the
	// empty list dashes, "Sign up for email newsletters",
	// the link-only "Most Popular" line.
	for _, drop := range []string{
		"Skip to content",
		"## Breaking News",
		"## Featured",
		"## Latest Headlines",
		"Sign up for email newsletters",
		"Most Popular",
	} {
		if strings.Contains(got, drop) {
			t.Errorf("cleaner failed to strip %q, got:\n%s", drop, got)
		}
	}

	// Must keep the real prose.
	for _, keep := range []string{
		"The real content of the article starts here.",
		"several",
		"paragraphs of real information",
		"concludes with a second paragraph",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("cleaner dropped legitimate prose %q, got:\n%s", keep, got)
		}
	}
}

func TestFormatMarkdown_EmptyInputs(t *testing.T) {
	if got := FormatMarkdown(nil); !strings.Contains(got, "No news digest") {
		t.Errorf("nil digest should produce placeholder, got %q", got)
	}
	if got := FormatMarkdown(&NewsDigest{}); !strings.Contains(got, "No profile fields") {
		t.Errorf("empty digest should produce placeholder, got %q", got)
	}
}

func TestCleanBodyForMarkdownReport_StripsLinkOnlyHeadings(t *testing.T) {
	input := `The real article body starts here and has meaningful content.

##   [Sports](https://example.com/sports/)

##   [Connecticut News](https://example.com/news/ct/)

## FREE FUN & GAMES

The article continues with more substance and a second paragraph of
genuinely relevant information for the operator.
`
	got := cleanBodyForMarkdownReport(input)

	for _, drop := range []string{
		"[Sports](https://example.com/sports/)",
		"[Connecticut News](https://example.com/news/ct/)",
		"FREE FUN & GAMES",
	} {
		if strings.Contains(got, drop) {
			t.Errorf("cleaner failed to strip %q, got:\n%s", drop, got)
		}
	}
	for _, keep := range []string{
		"The real article body starts here",
		"second paragraph of",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("cleaner dropped legitimate prose %q, got:\n%s", keep, got)
		}
	}
}

func TestCleanBodyForMarkdownReport_StripsNavPrefixConcatenated(t *testing.T) {
	// Publisher sometimes concatenates a nav anchor onto the
	// same line as the first body line, e.g. when the
	// extracted DOM didn't break the markup into separate
	// text nodes. The cleaner should detect "Skip to content"
	// as a prefix and drop the whole line.
	input := `Skip to content ## Breaking News ### [Headline text](https://example.com/h)

The real article body begins on a fresh line and continues
with actual reporting the operator wants to read.
`
	got := cleanBodyForMarkdownReport(input)

	if strings.Contains(got, "Skip to content") {
		t.Errorf("cleaner failed to strip leading nav phrase, got:\n%s", got)
	}
	if !strings.Contains(got, "The real article body begins") {
		t.Errorf("cleaner dropped legitimate prose, got:\n%s", got)
	}
}

func TestCleanBodyForMarkdownReport_StripsYouMayAlsoLikeList(t *testing.T) {
	// "You may also like" / related-articles list emitted
	// by publishers as a sequence of `### [headline](url)`
	// headings. The cleaner should drop all of them.
	input := `The real article body, two paragraphs of substantive content the
operator actually wants to read.

### [Some related article title](https://example.com/related-1/)

### [Another related headline](https://example.com/related-2/)

### [Yet another clickbait link](https://example.com/related-3/)

The article concludes with a final paragraph.
`
	got := cleanBodyForMarkdownReport(input)

	for _, drop := range []string{
		"related article title",
		"Another related headline",
		"clickbait link",
	} {
		if strings.Contains(got, drop) {
			t.Errorf("cleaner failed to strip related-article stub %q, got:\n%s", drop, got)
		}
	}
	for _, keep := range []string{
		"The real article body",
		"The article concludes with a final paragraph",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("cleaner dropped legitimate prose %q, got:\n%s", keep, got)
		}
	}
}

func TestCleanBodyForMarkdownReport_StripsPlainHeadingClusters(t *testing.T) {
	// Same as above but the publisher omitted the link
	// wrapping — the "you may also like" list is a cluster of
	// plain `### Headline` lines. The cluster-detection
	// pre-pass should drop them, but ONLY when the cluster
	// lives in the final 40% of the body. We pad the input
	// with a long real-prose preamble so the cluster falls
	// into the tail zone.
	longProse := strings.Repeat("This is a long paragraph of meaningful body text the operator actually wants to read and it contains a lot of real information about the topic at hand with many words and substantial sentences and continues to elaborate with more detail.\n\n", 12)
	input := longProse + `
### CT estate combines classic New England style

### Billionaires' plans for CT island hits opposition

### He co-founded an addiction treatment center

### Popular CT broadcaster lands new gig
`
	got := cleanBodyForMarkdownReport(input)

	for _, drop := range []string{
		"CT estate combines classic New England style",
		"Billionaires' plans for CT island",
		"He co-founded an addiction treatment center",
	} {
		if strings.Contains(got, drop) {
			t.Errorf("cleaner failed to drop related-headline cluster item %q, got:\n%s", drop, got)
		}
	}
}

func TestCleanBodyForMarkdownReport_PreservesMidBodyHeadingClusters(t *testing.T) {
	// When the cluster is in the MIDDLE of the body (e.g.
	// WeWorkRemotely / Indeed / YC Jobs job listing pages
	// where every job is a `###` subheading), preserve it.
	// Only TAIL clusters get dropped.
	input := `Real article body opening paragraph with many words
that the operator wants to read and that contains enough
text to count as a real body paragraph for the cleaner.

### Product Engineer

SuperPlane

USA

### AI-Native Software Developer

OnTheGoSystems

Remote

### Senior Fullstack Developer (Python)

Proxify AB

Sweden

The article then has a closing paragraph with enough
text content to count as a real body paragraph.
`
	got := cleanBodyForMarkdownReport(input)

	for _, keep := range []string{
		"Product Engineer",
		"AI-Native Software Developer",
		"Senior Fullstack Developer",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("cleaner over-aggressively dropped mid-body heading %q, got:\n%s", keep, got)
		}
	}
}

func TestCleanBodyForMarkdownReport_StripsEncodedGoogleTrackingLines(t *testing.T) {
	// Simulate the "full-text pull failed, RSS snippet
	// survived" failure mode: the body is full of Google
	// News tracking interstitials and `?continue=…`
	// percent-encoded redirects, repeated across many
	// paragraphs.
	input := `https://news.google.com/rss/articles/CBMiM2h0dHBzOi8vdGVjaGNydW5jaC5jb20vMjAyNi8wOC8wMS9vcGVuYWktbmV3LW1vZGVsL9IBAA?oc=5
continue=https%3A%2F%2Fwww.workatastartup.com%2Fapplication%3Fsignup_job_id%3D996646
continue=https%3A%2F%2Fwww.workatastartup.com%2Fapplication%3Fsignup_job_id%3D996528
continue=https%3A%2F%2Fwww.workatastartup.com%2Fapplication%3Fsignup_job_id%3D996528
engineer/remote
engineer/remote
engineer/remote

This is a real paragraph the operator wants to read about
what actually happened in the news today with several
sentences of substantive content.
`
	got := cleanBodyForMarkdownReport(input)

	// Must drop: every Google News tracking URL, every
	// percent-encoded `continue=` redirect, and the bare
	// `engineer/remote` link labels that publishers emit
	// when their nav is collapsed onto one line.
	for _, drop := range []string{
		"news.google.com/rss/articles",
		"%3A%2F%2F",
		"engineer/remote",
	} {
		if strings.Contains(got, drop) {
			t.Errorf("cleaner failed to strip %q, got:\n%s", drop, got)
		}
	}

	// Must keep: the real prose.
	for _, keep := range []string{
		"real paragraph the operator wants to read",
		"substantive content",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("cleaner dropped legitimate prose %q, got:\n%s", keep, got)
		}
	}
}

func TestCleanBodyForMarkdownReport_KeepsBenignPercentAndURLs(t *testing.T) {
	// Make sure the new "drop encoded URL lines" rule does
	// not over-fire. A normal article can legitimately
	// contain a "50% rise" or a single "see https://…"
	// reference. Those must survive.
	input := `Apple reported a 50% rise in services revenue this
quarter, the company said on Thursday, with the iPhone
install base now reaching an all-time high. Analysts
called the move a clear sign of platform strength.

For the full filing, see https://investor.apple.com/filing.
`
	got := cleanBodyForMarkdownReport(input)

	for _, keep := range []string{
		"50% rise in services revenue",
		"all-time high",
		"platform strength",
		"https://investor.apple.com/filing",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("cleaner over-aggressively dropped %q, got:\n%s", keep, got)
		}
	}
}

func TestCleanBodyForMarkdownReport_DropsMidBodyHeadlineClusterWithCurrency(t *testing.T) {
	// Regression: the cluster detector's
	// `shortHeadingRE` previously did not include the
	// `$` character, so a real-world "related stories"
	// cluster like the one Hartford Courant emits
	// (each line contains a price like "$6.8M" or a
	// year like "2026") silently slipped through the
	// cluster walk and the whole sidebar leaked into
	// the cleaned body. The fix added `$` to the
	// character class; this test pins the behavior.
	input := `## Hartford Courant

August 1, 2026 at 9:24 pm

Luke Bronin is known for his high-profile resume: Greenwich Country Day School, Philips Exeter Academy, Yale Law School, Rhodes Scholar, Hartford mayor for two terms.

### CT estate combines classic New England style with panoramic water views. It's listed for $6.8M

### Billionaires' plans for CT 'island' hits opposition. Invite everyone and it would 'be a disaster': neighbor

### He co-founded an addiction treatment center; she's a CT children's author. They're charged in a fatal overdose

### Popular CT broadcaster lands new gig. Here's what she's doing next. 'This was perfect for me.'

### Name the four UConn women's players named to this list of all-time WNBA franchise greats

### CT is dishing out $16.8M in grants to reconstruct 13 town bridges. See which ones will receive work.

### CT Supreme Court rules that, in some cases, non-patients can sue doctors for bad decisions

### CT park set to get state's first dedicated mountain bike trail. Plan includes incorporating 'natural' features

### Nursery turned destination CT restaurant has dining among blooms. 'It's like a Hallmark movie set': co-owner

### Central CT suburb tries to fight looming approval for unwelcome solar farm

The front seven figures to be UConn's defensive strength heading into 2026 as new defensive coordinator Ryan Manalac is tasked with putting the pieces together.
`
	got := cleanBodyForMarkdownReport(input)

	// The cluster of 10 `###` lines is the publisher's
	// "related stories" sidebar — must all be dropped.
	for _, drop := range []string{
		"CT estate combines classic New England style",
		"Billionaires' plans for CT",
		"He co-founded an addiction treatment center",
		"Popular CT broadcaster lands new gig",
		"Name the four UConn",
		"CT is dishing out $16.8M",
		"CT Supreme Court rules",
		"CT park set to get",
		"Nursery turned destination",
		"Central CT suburb tries to fight",
	} {
		if strings.Contains(got, drop) {
			t.Errorf("cleaner failed to drop sidebar cluster item %q, got:\n%s", drop, got)
		}
	}

	// Real prose survives.
	for _, keep := range []string{
		"Luke Bronin is known for his high-profile resume",
		"The front seven figures to be UConn's defensive strength",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("cleaner dropped legitimate prose %q, got:\n%s", keep, got)
		}
	}
}
