package stealth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// RobotsChecker caches and verifies robots.txt rules for target URLs.
type RobotsChecker struct {
	mu     sync.Mutex
	cache  map[string]*robotstxt.RobotsData
	client *http.Client
}

// DefaultRobotsChecker is a global instance for robots.txt checking.
var DefaultRobotsChecker = NewRobotsChecker()

// NewRobotsChecker initializes a RobotsChecker with custom HTTP client timeouts.
func NewRobotsChecker() *RobotsChecker {
	return &RobotsChecker{
		cache: make(map[string]*robotstxt.RobotsData),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// IsAllowed checks if targetURL is permitted for userAgent according to the domain's robots.txt.
func (r *RobotsChecker) IsAllowed(ctx context.Context, userAgent, targetURL string) (bool, error) {
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Host == "" {
		return true, nil // default allow on invalid URL
	}

	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := strings.ToLower(parsed.Host)

	r.mu.Lock()
	robotsData, found := r.cache[host]
	r.mu.Unlock()

	if !found {
		robotsURL := fmt.Sprintf("%s://%s/robots.txt", scheme, host)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
		if err != nil {
			return true, nil
		}
		if userAgent != "" {
			req.Header.Set("User-Agent", userAgent)
		}

		resp, err := r.client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			// If robots.txt cannot be fetched or returns non-200 (e.g. 404), allow scraping by standard practice
			r.mu.Lock()
			r.cache[host] = nil
			r.mu.Unlock()
			return true, nil
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // limit to 512KB
		_ = resp.Body.Close()
		if err != nil {
			return true, nil
		}

		data, err := robotstxt.FromBytes(body)
		if err != nil {
			data = nil
		}

		r.mu.Lock()
		r.cache[host] = data
		robotsData = data
		r.mu.Unlock()
	}

	if robotsData == nil {
		return true, nil
	}

	group := robotsData.FindGroup(userAgent)
	reqPath := parsed.Path
	if reqPath == "" {
		reqPath = "/"
	}
	if parsed.RawQuery != "" {
		reqPath += "?" + parsed.RawQuery
	}

	return group.Test(reqPath), nil
}
