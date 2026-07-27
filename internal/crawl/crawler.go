package crawl

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/kaiizer-99/onyx-scrapper/internal/browser"
	"github.com/kaiizer-99/onyx-scrapper/internal/extract"
	"github.com/kaiizer-99/onyx-scrapper/internal/llm"
	"github.com/kaiizer-99/onyx-scrapper/internal/stealth"
	"github.com/kaiizer-99/onyx-scrapper/internal/store"
)

// CrawlOptions defines configuration for a multi-page site crawl.
type CrawlOptions struct {
	StartURL      string
	MaxPages      int
	MaxDepth      int
	Workers       int
	Render        bool
	Schema        string
	Store         *store.Store
	LLMClient     *llm.Client
	BrowserPool   *browser.Pool
	OnPageCrawled func(pageURL string, count int, err error)
}

// CrawledPageInfo contains metadata for a single crawled page.
type CrawledPageInfo struct {
	URL          string `json:"url"`
	PageID       int64  `json:"page_id,omitempty"`
	Depth        int    `json:"depth"`
	CleanBytes   int    `json:"clean_bytes"`
	ExtractionID int64  `json:"extraction_id,omitempty"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

// CrawlResult holds the final summary output of a site crawl.
type CrawlResult struct {
	StartURL        string            `json:"start_url"`
	TargetHost      string            `json:"target_host"`
	TotalDiscovered int               `json:"total_discovered"`
	TotalCrawled    int               `json:"total_crawled"`
	TotalSaved      int               `json:"total_saved"`
	TotalFailed     int               `json:"total_failed"`
	WorkersUsed     int               `json:"workers_used"`
	DurationMs      int64             `json:"duration_ms"`
	Pages           []CrawledPageInfo `json:"pages"`
	QueueStats      QueueStats        `json:"queue_stats"`
}

// Crawler manages site discovery, queue execution, page scraping, and storage.
type Crawler struct {
	discoverer *Discoverer
}

// NewCrawler constructs a new Crawler.
func NewCrawler() *Crawler {
	return &Crawler{
		discoverer: NewDiscoverer(),
	}
}

// Crawl executes multi-page discovery and ingestion for target site based on CrawlOptions.
func (c *Crawler) Crawl(ctx context.Context, opts CrawlOptions) (*CrawlResult, error) {
	if opts.StartURL == "" {
		return nil, fmt.Errorf("start_url is required")
	}

	if opts.MaxPages <= 0 {
		opts.MaxPages = 50
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 3
	}
	if opts.Workers <= 0 {
		opts.Workers = 5
	}

	startTime := time.Now()
	parsedStart, err := url.Parse(opts.StartURL)
	if err != nil || parsedStart.Host == "" {
		return nil, fmt.Errorf("invalid start URL %q: %v", opts.StartURL, err)
	}
	targetHost := strings.ToLower(parsedStart.Host)

	// Create browser worker pool if rendering is required or multiple workers requested
	var pool *browser.Pool
	if opts.BrowserPool != nil {
		pool = opts.BrowserPool
	} else if opts.Render || opts.Workers > 1 {
		pool = browser.NewPool(opts.Workers)
		defer pool.Close()
	}

	q := NewQueue()
	slog.Info("Starting site crawling & discovery", "start_url", opts.StartURL, "max_pages", opts.MaxPages, "max_depth", opts.MaxDepth, "workers", opts.Workers)

	// Step 1: Sitemap Discovery
	discoveredURLs, discErr := c.discoverer.DiscoverURLs(ctx, opts.StartURL)
	if discErr != nil {
		slog.Warn("Sitemap discovery encountered warning", "error", discErr)
	}

	totalDiscovered := len(discoveredURLs)
	if totalDiscovered > 0 {
		slog.Info("Sitemap discovery found URLs", "count", totalDiscovered)
		q.EnqueueBatch(discoveredURLs, 1)
	}

	// Always ensure start URL is enqueued at depth 0
	q.Enqueue(opts.StartURL, 0)

	result := &CrawlResult{
		StartURL:    opts.StartURL,
		TargetHost:  targetHost,
		WorkersUsed: opts.Workers,
		Pages:       make([]CrawledPageInfo, 0),
	}

	var (
		mu           sync.Mutex
		claimedCount int
		totalCrawled int
		totalSaved   int
		totalFailed  int
		activeCount  int
		cond         = sync.NewCond(&mu)
		resultsMap   = make(map[string]CrawledPageInfo)
		pageOrder    []string
		wg           sync.WaitGroup
	)

	// Context cancellation monitoring goroutine to unblock waiting workers
	cancelDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			mu.Lock()
			cond.Broadcast()
			mu.Unlock()
		case <-cancelDone:
		}
	}()
	defer close(cancelDone)

	// Launch concurrent crawler worker pool
	for w := 0; w < opts.Workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				mu.Lock()
				if claimedCount >= opts.MaxPages {
					cond.Broadcast()
					mu.Unlock()
					return
				}

				item := q.Pop()
				if item == nil {
					stats := q.Stats()
					if activeCount == 0 && stats.Pending == 0 {
						cond.Broadcast()
						mu.Unlock()
						return
					}

					cond.Wait()
					mu.Unlock()
					continue
				}

				claimedCount++
				activeCount++
				mu.Unlock()

				// Process single page task
				pageInfo, saved, failed, childLinks, pErr := c.processCrawlItem(ctx, item, opts, targetHost, pool, q)

				mu.Lock()
				activeCount--
				if failed {
					totalFailed++
				} else {
					totalCrawled++
					if saved {
						totalSaved++
					}
				}

				resultsMap[item.URL] = pageInfo
				pageOrder = append(pageOrder, item.URL)

				if opts.OnPageCrawled != nil {
					opts.OnPageCrawled(item.URL, totalCrawled+totalFailed, pErr)
				}

				if len(childLinks) > 0 && claimedCount < opts.MaxPages {
					added := q.EnqueueBatch(childLinks, item.Depth+1)
					if added > 0 {
						slog.Debug("Enqueued child links", "worker", workerID, "source", item.URL, "added", added)
					}
				}

				cond.Broadcast()
				mu.Unlock()
			}
		}(w)
	}

	wg.Wait()

	// Assemble final ordered results
	for _, u := range pageOrder {
		if pInfo, ok := resultsMap[u]; ok {
			result.Pages = append(result.Pages, pInfo)
		}
	}

	result.TotalDiscovered = totalDiscovered + q.Stats().Total
	result.TotalCrawled = totalCrawled
	result.TotalSaved = totalSaved
	result.TotalFailed = totalFailed
	result.DurationMs = time.Since(startTime).Milliseconds()
	result.QueueStats = q.Stats()

	slog.Info("Crawl completed", "crawled", totalCrawled, "saved", totalSaved, "failed", totalFailed, "workers", opts.Workers, "duration_ms", result.DurationMs)

	return result, nil
}

// processCrawlItem fetches, cleans, extracts, and persists a single crawled page item.
func (c *Crawler) processCrawlItem(ctx context.Context, item *QueueItem, opts CrawlOptions, targetHost string, pool *browser.Pool, q *Queue) (CrawledPageInfo, bool, bool, []string, error) {
	// Verify robots.txt permission
	allowed, _ := stealth.DefaultRobotsChecker.IsAllowed(ctx, "OnyxBot", item.URL)
	if !allowed {
		errRobots := fmt.Errorf("disallowed by robots.txt")
		q.MarkFailed(item.URL, errRobots)
		return CrawledPageInfo{
			URL:    item.URL,
			Depth:  item.Depth,
			Status: "failed",
			Error:  errRobots.Error(),
		}, false, true, nil, errRobots
	}

	slog.Info("Crawling page", "url", item.URL, "depth", item.Depth)

	// Fetch page HTML using browser pool (or static fetch)
	rawHTML, _, fetchErr := extract.FetchWithPool(ctx, pool, item.URL, opts.Render)
	if fetchErr != nil {
		q.MarkFailed(item.URL, fetchErr)
		return CrawledPageInfo{
			URL:    item.URL,
			Depth:  item.Depth,
			Status: "failed",
			Error:  fetchErr.Error(),
		}, false, true, nil, fetchErr
	}

	cleanText, cleanErr := extract.CleanHTML(rawHTML)
	if cleanErr != nil {
		cleanText = rawHTML
	}

	pageInfo := CrawledPageInfo{
		URL:        item.URL,
		Depth:      item.Depth,
		CleanBytes: len(cleanText),
		Status:     "crawled",
	}

	saved := false
	if opts.Store != nil {
		pageID, saveErr := opts.Store.SavePage(item.URL, rawHTML, cleanText)
		if saveErr != nil {
			slog.Warn("Failed to save crawled page to DB", "url", item.URL, "error", saveErr)
		} else {
			pageInfo.PageID = pageID
			saved = true
		}

		if opts.Schema != "" && opts.LLMClient != nil {
			rawJSON, extErr := extract.ExtractJSON(ctx, opts.LLMClient, rawHTML, opts.Schema)
			if extErr != nil {
				slog.Warn("Structured extraction failed during crawl", "url", item.URL, "schema", opts.Schema, "error", extErr)
			} else {
				extID, _ := opts.Store.SaveExtraction(pageID, opts.Schema, string(rawJSON))
				pageInfo.ExtractionID = extID
			}
		}
	}

	q.MarkDone(item.URL)

	var childLinks []string
	if item.Depth < opts.MaxDepth {
		childLinks = extractChildLinks(item.URL, rawHTML, targetHost)
	}

	return pageInfo, saved, false, childLinks, nil
}

// extractChildLinks finds all same-host <a href> links inside HTML content.
func extractChildLinks(baseURL, rawHTML, targetHost string) []string {
	parsedBase, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rawHTML))
	if err != nil {
		return nil
	}

	linkMap := make(map[string]bool)
	var links []string

	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}

		href = strings.TrimSpace(href)
		if href == "" || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") {
			return
		}

		relURL, err := url.Parse(href)
		if err != nil {
			return
		}

		resolved := parsedBase.ResolveReference(relURL)
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			return
		}

		if strings.EqualFold(resolved.Host, targetHost) {
			resolved.Fragment = "" // Strip anchor
			fullStr := resolved.String()
			if !linkMap[fullStr] {
				linkMap[fullStr] = true
				links = append(links, fullStr)
			}
		}
	})

	return links
}
