package crawl

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kaiizer-99/onyx-scrapper/internal/stealth"
)

// URLSet represents a standard XML sitemap containing page URLs.
type URLSet struct {
	XMLName xml.Name  `xml:"urlset"`
	URLs    []URLItem `xml:"url"`
}

// URLItem represents an individual URL entry in a sitemap.
type URLItem struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

// SitemapIndex represents an XML sitemap index pointing to sub-sitemaps.
type SitemapIndex struct {
	XMLName  xml.Name      `xml:"sitemapindex"`
	Sitemaps []SitemapItem `xml:"sitemap"`
}

// SitemapItem represents a nested sitemap reference in a sitemap index.
type SitemapItem struct {
	Loc string `xml:"loc"`
}

// Discoverer handles sitemap detection and link harvesting for a domain.
type Discoverer struct {
	httpClient *http.Client
}

// NewDiscoverer creates a Discoverer with default timeout settings.
func NewDiscoverer() *Discoverer {
	return &Discoverer{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// DiscoverURLs extracts all available sitemap URLs for startURL's domain.
func (d *Discoverer) DiscoverURLs(ctx context.Context, startURL string) ([]string, error) {
	parsed, err := url.Parse(startURL)
	if err != nil {
		return nil, fmt.Errorf("invalid start URL %q: %w", startURL, err)
	}

	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := strings.ToLower(parsed.Host)
	baseURL := fmt.Sprintf("%s://%s", scheme, host)

	// Step 1: Check robots.txt for Sitemap directives
	sitemapURLs := d.fetchSitemapsFromRobots(ctx, baseURL)

	// Step 2: Add common sitemap fallbacks if no sitemaps found
	if len(sitemapURLs) == 0 {
		sitemapURLs = append(sitemapURLs,
			fmt.Sprintf("%s/sitemap.xml", baseURL),
			fmt.Sprintf("%s/sitemap_index.xml", baseURL),
			fmt.Sprintf("%s/sitemap.xml.gz", baseURL),
		)
	}

	// Step 3: Fetch and parse sitemap URLs
	visitedSitemaps := make(map[string]bool)
	discoveredMap := make(map[string]bool)
	var resultURLs []string

	for _, sitemapURL := range sitemapURLs {
		urls, err := d.fetchAndParseSitemapRecursive(ctx, sitemapURL, host, visitedSitemaps, 0)
		if err != nil {
			continue
		}
		for _, u := range urls {
			if !discoveredMap[u] {
				// Check robots.txt compliance
				allowed, _ := stealth.DefaultRobotsChecker.IsAllowed(ctx, "OnyxBot", u)
				if allowed {
					discoveredMap[u] = true
					resultURLs = append(resultURLs, u)
				}
			}
		}
	}

	return resultURLs, nil
}

// fetchSitemapsFromRobots fetches robots.txt and extracts Sitemap: directives.
func (d *Discoverer) fetchSitemapsFromRobots(ctx context.Context, baseURL string) []string {
	robotsURL := fmt.Sprintf("%s/robots.txt", baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "OnyxBot/1.0 (+https://github.com/kaiizer-99/onyx-scrapper)")

	resp, err := d.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil
	}

	baseParsed, _ := url.Parse(baseURL)

	var sitemaps []string
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "sitemap:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				sm := strings.TrimSpace(parts[1])
				if sm != "" {
					if !strings.HasPrefix(sm, "http://") && !strings.HasPrefix(sm, "https://") {
						if baseParsed != nil {
							u, err := url.Parse(sm)
							if err == nil {
								sm = baseParsed.ResolveReference(u).String()
							} else {
								sm = fmt.Sprintf("%s/%s", strings.TrimSuffix(baseURL, "/"), strings.TrimPrefix(sm, "/"))
							}
						}
					}
					sitemaps = append(sitemaps, sm)
				}
			}
		}
	}

	return sitemaps
}

// fetchAndParseSitemapRecursive parses a sitemap or sitemapindex up to depth 3.
func (d *Discoverer) fetchAndParseSitemapRecursive(ctx context.Context, sitemapURL, targetHost string, visited map[string]bool, depth int) ([]string, error) {
	if depth > 3 || visited[sitemapURL] {
		return nil, nil
	}
	visited[sitemapURL] = true

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sitemapURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "OnyxBot/1.0 (+https://github.com/kaiizer-99/onyx-scrapper)")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitemap %s returned HTTP %d", sitemapURL, resp.StatusCode)
	}

	var reader io.Reader = resp.Body
	if strings.HasSuffix(strings.ToLower(sitemapURL), ".gz") || resp.Header.Get("Content-Type") == "application/x-gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read gzipped sitemap: %w", err)
		}
		defer gzReader.Close()
		// Copy buffer to allow unmarshaling
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, gzReader); err != nil {
			return nil, err
		}
		return d.parseSitemapContent(buf.Bytes(), sitemapURL, targetHost, visited, depth)
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(reader, 10*1024*1024)) // 10MB limit
	if err != nil {
		return nil, err
	}

	return d.parseSitemapContent(bodyBytes, sitemapURL, targetHost, visited, depth)
}

func (d *Discoverer) parseSitemapContent(bodyBytes []byte, sitemapURL, targetHost string, visited map[string]bool, depth int) ([]string, error) {
	// Try parsing as SitemapIndex first
	var idx SitemapIndex
	if err := xml.Unmarshal(bodyBytes, &idx); err == nil && len(idx.Sitemaps) > 0 {
		var allURLs []string
		for _, sm := range idx.Sitemaps {
			subLoc := strings.TrimSpace(sm.Loc)
			if subLoc != "" {
				urls, err := d.fetchAndParseSitemapRecursive(context.Background(), subLoc, targetHost, visited, depth+1)
				if err == nil {
					allURLs = append(allURLs, urls...)
				}
			}
		}
		return allURLs, nil
	}

	// Try parsing as URLSet
	var uSet URLSet
	if err := xml.Unmarshal(bodyBytes, &uSet); err == nil && len(uSet.URLs) > 0 {
		var validURLs []string
		for _, item := range uSet.URLs {
			loc := strings.TrimSpace(item.Loc)
			if loc != "" && isSameHost(loc, targetHost) {
				validURLs = append(validURLs, loc)
			}
		}
		return validURLs, nil
	}

	// Fallback regex/string parsing for loosely formatted XML loc tags
	return extractLocTags(bodyBytes, targetHost), nil
}

func isSameHost(targetURL, targetHost string) bool {
	p, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(p.Host, targetHost)
}

func extractLocTags(body []byte, targetHost string) []string {
	var urls []string
	content := string(body)
	startTag := "<loc>"
	endTag := "</loc>"

	idx := 0
	for {
		sPos := strings.Index(content[idx:], startTag)
		if sPos == -1 {
			break
		}
		sPos += idx + len(startTag)
		ePos := strings.Index(content[sPos:], endTag)
		if ePos == -1 {
			break
		}
		ePos += sPos

		u := strings.TrimSpace(content[sPos:ePos])
		if u != "" && isSameHost(u, targetHost) {
			urls = append(urls, u)
		}
		idx = ePos + len(endTag)
	}

	return urls
}
