package stealth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// DomainRateLimiter maintains per-host rate limiters.
type DomainRateLimiter struct {
	mu          sync.Mutex
	limiters    map[string]*rate.Limiter
	interval    time.Duration
	burst       int
}

// NewDomainRateLimiter initializes a rate limiter with custom request interval and burst.
func NewDomainRateLimiter(interval time.Duration, burst int) *DomainRateLimiter {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if burst <= 0 {
		burst = 1
	}
	return &DomainRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		interval: interval,
		burst:    burst,
	}
}

// DefaultDomainRateLimiter is a global rate limiter allowing 1 request per 2 seconds per domain.
var DefaultDomainRateLimiter = NewDomainRateLimiter(2*time.Second, 1)

// GetLimiter gets or creates a rate limiter for target domain host.
func (d *DomainRateLimiter) GetLimiter(host string) *rate.Limiter {
	host = strings.ToLower(strings.TrimSpace(host))
	// strip port if present
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	limiter, exists := d.limiters[host]
	if !exists {
		limiter = rate.NewLimiter(rate.Every(d.interval), d.burst)
		d.limiters[host] = limiter
	}
	return limiter
}

// Wait blocks until the rate limiter permits a request for targetURL's domain host.
func (d *DomainRateLimiter) Wait(ctx context.Context, targetURL string) error {
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Host == "" {
		return nil // skip rate limit if URL cannot be parsed
	}

	limiter := d.GetLimiter(parsed.Host)
	if err := limiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limit wait error for host %s: %w", parsed.Host, err)
	}
	return nil
}
