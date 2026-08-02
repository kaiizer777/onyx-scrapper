package news

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/extract"
)

// RSSArticle represents a single news headline parsed from Google News RSS.
type RSSArticle struct {
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	// GoogleRedirectURL is the raw <link> value from the RSS
	// feed. It is a Google News tracking interstitial that
	// requires JS to resolve to the real publisher article.
	// Surfaced for diagnostics; the URL the operator / fetch
	// pipeline should use is the publisher URL from
	// <source url="…">, which we copy into URL.
	GoogleRedirectURL string    `json:"google_redirect_url,omitempty"`
	Source            string    `json:"source"`
	SourceURL         string    `json:"source_url"`
	PublishedAt       time.Time `json:"published_at"`
	Snippet           string    `json:"snippet"`
}

type rssResponse struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string    `xml:"title"`
		Link  string    `xml:"link"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	PubDate     string    `xml:"pubDate"`
	Description string    `xml:"description"`
	Source      rssSource `xml:"source"`
}

type rssSource struct {
	URL  string `xml:"url,attr"`
	Text string `xml:",chardata"`
}

// BuildGoogleNewsQuery constructs the Google News RSS query string from keywords and recency tag.
func BuildGoogleNewsQuery(keywordsCSV string, googleNewsWhen string) string {
	parts := strings.Split(keywordsCSV, ",")
	var cleaned []string
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			if strings.Contains(t, " ") && !strings.HasPrefix(t, `"`) {
				cleaned = append(cleaned, fmt.Sprintf(`"%s"`, t))
			} else {
				cleaned = append(cleaned, t)
			}
		}
	}

	var q string
	if len(cleaned) == 1 {
		q = cleaned[0]
	} else if len(cleaned) > 1 {
		q = "(" + strings.Join(cleaned, " OR ") + ")"
	}

	googleNewsWhen = strings.TrimSpace(googleNewsWhen)
	if googleNewsWhen != "" {
		if !strings.HasPrefix(googleNewsWhen, "when:") {
			googleNewsWhen = "when:" + googleNewsWhen
		}
		if q != "" {
			q += " " + googleNewsWhen
		} else {
			q = googleNewsWhen
		}
	}

	return q
}

// FetchGoogleNewsRSS fetches news articles from Google News RSS for given keywords and recency tag.
func FetchGoogleNewsRSS(ctx context.Context, httpClient *http.Client, keywordsCSV string, googleNewsWhen string) ([]RSSArticle, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	q := BuildGoogleNewsQuery(keywordsCSV, googleNewsWhen)
	if q == "" {
		return nil, fmt.Errorf("empty news search query")
	}

	endpoint := fmt.Sprintf("https://news.google.com/rss/search?q=%s&hl=en-US&gl=US&ceid=US:en", url.QueryEscape(q))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create RSS request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("RSS HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google News RSS returned HTTP status %d", resp.StatusCode)
	}

	var rssPayload rssResponse
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&rssPayload); err != nil {
		return nil, fmt.Errorf("failed to decode RSS XML: %w", err)
	}

	articles := make([]RSSArticle, 0, len(rssPayload.Channel.Items))
	for _, item := range rssPayload.Channel.Items {
		art := parseRSSItem(item)
		if art.Title != "" && art.URL != "" {
			articles = append(articles, art)
		}
	}

	return articles, nil
}

func parseRSSItem(item rssItem) RSSArticle {
	title := strings.TrimSpace(item.Title)
	link := strings.TrimSpace(item.Link)
	sourceName := strings.TrimSpace(item.Source.Text)
	sourceURL := strings.TrimSpace(item.Source.URL)

	// Fallback to extract source from title if sourceName is empty (e.g., "Title - Source")
	if sourceName == "" && strings.Contains(title, " - ") {
		idx := strings.LastIndex(title, " - ")
		if idx != -1 && idx < len(title)-3 {
			sourceName = strings.TrimSpace(title[idx+3:])
		}
	}

	cleanSnippet, _ := extract.CleanHTML(item.Description)
	cleanSnippet = strings.TrimSpace(cleanSnippet)

	pubTime := parsePubDate(item.PubDate)

	// Prefer the publisher URL from <source url="…"> over the
	// Google News redirect URL stored in <link>. The redirect
	// is a tracking interstitial that requires JS to resolve,
	// so it both (a) gives the operator a useless "click
	// through" target and (b) blocks the orchestrator's
	// full-text fetch (Colly/rod hit a JS interstitial
	// instead of the actual article). Falling back to <link>
	// only when the source URL is empty preserves the
	// original behaviour for malformed feeds.
	effectiveURL := sourceURL
	if effectiveURL == "" {
		effectiveURL = link
	}

	// Rewrite the snippet's link target from the Google News
	// tracking interstitial to the publisher URL. The Google
	// News RSS <description> is rendered by CleanHTML into a
	// markdown link like
	//   [OpenAI Releases New Frontier Model](https://news.google.com/rss/articles/CBMi…)
	// which becomes the article's "Summary" / "Body" when the
	// full-text pull fails. Re-pointing that link at the
	// publisher URL means even the fallback snippet points at
	// something the operator can actually read.
	if sourceURL != "" && link != sourceURL {
		cleanSnippet = rewriteSnippetLink(cleanSnippet, link, sourceURL)
	}

	return RSSArticle{
		Title:       title,
		URL:         effectiveURL,
		// GoogleRedirectURL keeps the original <link> value
		// around for diagnostics / future "open in Google
		// News" affordances. Empty when <source url> was
		// already the link.
		GoogleRedirectURL: link,
		Source:      sourceName,
		SourceURL:   sourceURL,
		PublishedAt: pubTime,
		Snippet:     cleanSnippet,
	}
}

func parsePubDate(pubDateStr string) time.Time {
	pubDateStr = strings.TrimSpace(pubDateStr)
	if pubDateStr == "" {
		return time.Time{}
	}

	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"Mon, 02 Jan 2006 15:04:05 MST",
		"02 Jan 2006 15:04:05 MST",
	}

	for _, fmtStr := range formats {
		if t, err := time.Parse(fmtStr, pubDateStr); err == nil {
			return t
		}
	}

	return time.Time{}
}

// snippetLinkRE matches a markdown link `[text](url)` exactly —
// we use it to find the link inside the cleaned RSS snippet and
// swap its target from the Google News tracking URL to the
// publisher URL. The regex is intentionally narrow (no nested
// parens, no escaped brackets) because the snippet shape coming
// out of extract.CleanHTML is always a single plain markdown
// link, not a complex inline expression.
var snippetLinkRE = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// rewriteSnippetLink swaps the URL of the first markdown link
// in the snippet from the Google News tracking interstitial
// (oldURL) to the publisher URL (newURL). When the snippet has
// no link, or the link's URL doesn't match oldURL, the snippet
// is returned unchanged.
func rewriteSnippetLink(snippet, oldURL, newURL string) string {
	if snippet == "" || oldURL == "" || newURL == "" || oldURL == newURL {
		return snippet
	}
	return snippetLinkRE.ReplaceAllStringFunc(snippet, func(match string) string {
		sub := snippetLinkRE.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		text, url := sub[1], sub[2]
		// Compare normalized URLs to avoid missing the match
		// because of a trailing slash, fragment, or query
		// param difference.
		if normalizeForSnippetCompare(url) != normalizeForSnippetCompare(oldURL) {
			return match
		}
		return fmt.Sprintf("[%s](%s)", text, newURL)
	})
}

// normalizeForSnippetCompare trims, lowercases, and strips a
// trailing slash / fragment so two URLs that refer to the same
// resource compare equal.
func normalizeForSnippetCompare(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.ToLower(rawURL)
	}
	u.Fragment = ""
	return strings.ToLower(strings.TrimRight(u.String(), "/"))
}
