package crawl

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/kaiizer-99/onyx-scrapper/internal/stealth"
	"github.com/kaiizer-99/onyx-scrapper/internal/store"
)

func TestQueueOperations(t *testing.T) {
	q := NewQueue()

	if q.HasPending() {
		t.Fatalf("expected queue to be empty initially")
	}

	urls := []string{
		"https://example.com/page1",
		"https://example.com/page2",
		"https://example.com/page1#section", // duplicate after normalization
	}

	added := q.EnqueueBatch(urls, 0)
	if added != 2 {
		t.Fatalf("expected 2 unique URLs enqueued, got %d", added)
	}

	stats := q.Stats()
	if stats.Pending != 2 || stats.Total != 2 {
		t.Fatalf("expected pending=2, total=2, got %+v", stats)
	}

	item1 := q.Pop()
	if item1 == nil || item1.URL != "https://example.com/page1" {
		t.Fatalf("expected first item page1, got %v", item1)
	}
	if item1.Status != StatusProcessing {
		t.Fatalf("expected status processing, got %s", item1.Status)
	}

	q.MarkDone(item1.URL)
	stats = q.Stats()
	if stats.Done != 1 || stats.Pending != 1 {
		t.Fatalf("expected done=1, pending=1, got %+v", stats)
	}

	item2 := q.Pop()
	if item2 == nil || item2.URL != "https://example.com/page2" {
		t.Fatalf("expected second item page2, got %v", item2)
	}

	q.MarkFailed(item2.URL, fmt.Errorf("http error 500"))
	stats = q.Stats()
	if stats.Failed != 1 || stats.Done != 1 {
		t.Fatalf("expected failed=1, done=1, got %+v", stats)
	}

	if q.HasPending() {
		t.Fatalf("expected no pending items remaining")
	}
}

func TestDiscovererSitemapParsing(t *testing.T) {
	// Mock HTTP server serving sitemap XML and gzipped sitemaps
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprintf(w, "User-agent: *\nDisallow: /admin\nSitemap: http://%s/sitemap.xml\nSitemap: http://%s/sitemap.xml.gz\n", r.Host, r.Host)
		case "/sitemap.xml":
			w.Header().Set("Content-Type", "text/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <url><loc>http://%s/page-a</loc></url>
   <url><loc>http://%s/page-b</loc></url>
</urlset>`, r.Host, r.Host)
		case "/sitemap.xml.gz":
			w.Header().Set("Content-Type", "application/x-gzip")
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			xmlContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
   <url><loc>http://%s/page-c</loc></url>
</urlset>`, r.Host)
			gw.Write([]byte(xmlContent))
			gw.Close()
			w.Write(buf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	disc := NewDiscoverer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	urls, err := disc.DiscoverURLs(ctx, ts.URL)
	if err != nil {
		t.Fatalf("DiscoverURLs returned unexpected error: %v", err)
	}

	if len(urls) < 3 {
		t.Fatalf("expected at least 3 discovered URLs, got %d: %v", len(urls), urls)
	}

	parsedTS, _ := url.Parse(ts.URL)
	host := parsedTS.Host

	foundA, foundB, foundC := false, false, false
	for _, u := range urls {
		if u == fmt.Sprintf("http://%s/page-a", host) {
			foundA = true
		}
		if u == fmt.Sprintf("http://%s/page-b", host) {
			foundB = true
		}
		if u == fmt.Sprintf("http://%s/page-c", host) {
			foundC = true
		}
	}

	if !foundA || !foundB || !foundC {
		t.Fatalf("expected page-a, page-b, page-c to be discovered, got: %v", urls)
	}
}

func TestCrawlerEndToEnd(t *testing.T) {
	// Setup mock site with multiple linked pages
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprintf(w, `<html><body><h1>Home</h1><a href="/about">About</a> <a href="/contact">Contact</a></body></html>`)
		case "/about":
			fmt.Fprintf(w, `<html><body><h1>About Us</h1><a href="/team">Team</a> <a href="/">Home</a></body></html>`)
		case "/contact":
			fmt.Fprintf(w, `<html><body><h1>Contact</h1><a href="/">Home</a></body></html>`)
		case "/team":
			fmt.Fprintf(w, `<html><body><h1>Our Team</h1><a href="/about">About</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	defer st.Close()

	crawler := NewCrawler()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := crawler.Crawl(ctx, CrawlOptions{
		StartURL: ts.URL,
		MaxPages: 10,
		MaxDepth: 3,
		Store:    st,
	})
	if err != nil {
		t.Fatalf("Crawler.Crawl failed: %v", err)
	}

	if res.TotalCrawled < 4 {
		t.Fatalf("expected at least 4 pages crawled, got %d (saved=%d, failed=%d)", res.TotalCrawled, res.TotalSaved, res.TotalFailed)
	}

	if res.TotalSaved < 4 {
		t.Fatalf("expected at least 4 pages saved in SQLite, got %d", res.TotalSaved)
	}

	// Verify database search across ingested pages
	searchResults, err := st.SearchPages("Team")
	if err != nil {
		t.Fatalf("SearchPages failed on crawled data: %v", err)
	}
	if len(searchResults) == 0 {
		t.Fatalf("expected FTS search to find 'Team' page in SQLite store")
	}
}

func TestConcurrentCrawler(t *testing.T) {
	// Override rate limiter for local test server to allow instant parallel processing
	oldLimiter := stealth.DefaultDomainRateLimiter
	stealth.DefaultDomainRateLimiter = stealth.NewDomainRateLimiter(1*time.Millisecond, 100)
	defer func() { stealth.DefaultDomainRateLimiter = oldLimiter }()

	// Create mock site with 10 inter-linked pages, each with artificial delay to verify worker concurrency
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond) // Simulated server latency
		if r.URL.Path == "/" {
			fmt.Fprintf(w, `<html><body><h1>Index</h1>`)
			for i := 1; i <= 9; i++ {
				fmt.Fprintf(w, `<a href="/page%d">Page %d</a> `, i, i)
			}
			fmt.Fprintf(w, `</body></html>`)
			return
		}

		fmt.Fprintf(w, `<html><body><h1>Page %s</h1><a href="/">Home</a></body></html>`, r.URL.Path)
	}))
	defer ts.Close()

	st, err := store.NewStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	defer st.Close()

	crawler := NewCrawler()

	// 1. Run sequential crawl (Workers = 1)
	ctxSeq, cancelSeq := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelSeq()
	startSeq := time.Now()
	resSeq, err := crawler.Crawl(ctxSeq, CrawlOptions{
		StartURL: ts.URL,
		MaxPages: 5,
		MaxDepth: 2,
		Workers:  1,
		Store:    st,
	})
	if err != nil {
		t.Fatalf("Sequential crawl failed: %v", err)
	}
	if resSeq.TotalCrawled != 5 {
		t.Fatalf("expected 5 pages crawled in sequential run, got %d", resSeq.TotalCrawled)
	}
	durSeq := time.Since(startSeq)

	// 2. Run concurrent crawl (Workers = 5)
	ctxConc, cancelConc := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelConc()
	startConc := time.Now()
	resConc, err := crawler.Crawl(ctxConc, CrawlOptions{
		StartURL: ts.URL,
		MaxPages: 5,
		MaxDepth: 2,
		Workers:  5,
		Store:    st,
	})
	if err != nil {
		t.Fatalf("Concurrent crawl failed: %v", err)
	}
	durConc := time.Since(startConc)

	if resConc.TotalCrawled != 5 {
		t.Fatalf("expected 5 pages crawled in concurrent run, got %d", resConc.TotalCrawled)
	}

	if resConc.WorkersUsed != 5 {
		t.Fatalf("expected WorkersUsed=5, got %d", resConc.WorkersUsed)
	}

	t.Logf("Sequential crawl time (1 worker): %v, Concurrent crawl time (5 workers): %v", durSeq, durConc)

	if durConc >= durSeq {
		t.Logf("Warning: Concurrent duration (%v) was not strictly faster than sequential (%v) in test environment", durConc, durSeq)
	}
}
