package browser

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	stealthpkg "github.com/kaiizer777/onyx-scrapper/internal/stealth"
)

// Pool manages a worker pool of concurrent browser pages (tabs) backed by a shared stealth Chromium instance.
type Pool struct {
	mu         sync.Mutex
	maxWorkers int
	sem        chan struct{}
	browser    *rod.Browser
	launcher   *launcher.Launcher
	closed     bool
}

// NewPool constructs a new browser Pool with the specified maximum concurrency limit.
func NewPool(maxWorkers int) *Pool {
	if maxWorkers <= 0 {
		maxWorkers = runtime.NumCPU()
		if maxWorkers < 2 {
			maxWorkers = 2
		}
		if maxWorkers > 10 {
			maxWorkers = 10
		}
	}

	return &Pool{
		maxWorkers: maxWorkers,
		sem:        make(chan struct{}, maxWorkers),
	}
}

// MaxWorkers returns the configured maximum concurrency for this pool.
func (p *Pool) MaxWorkers() int {
	return p.maxWorkers
}

// getBrowser lazily initializes and returns the shared rod.Browser instance.
func (p *Pool) getBrowser() (*rod.Browser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, fmt.Errorf("browser pool is closed")
	}

	if p.browser != nil {
		return p.browser, nil
	}

	l := launcher.New().
		Headless(true).
		Leakless(false)

	if os.Getenv("CHROMIUM_NO_SANDBOX") == "1" {
		l = l.Set("no-sandbox").Bin("/usr/bin/chromium")
	}

	url, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch chromium browser for pool: %w", err)
	}

	b := rod.New().ControlURL(url)
	if err := b.Connect(); err != nil {
		l.Kill()
		return nil, fmt.Errorf("failed to connect to chromium browser in pool: %w", err)
	}

	p.launcher = l
	p.browser = b
	return p.browser, nil
}

// FetchRendered acquires a pool slot, creates a stealth page tab on the shared browser instance,
// navigates to targetURL, waits for rendering, and returns HTML.
func (p *Pool) FetchRendered(ctx context.Context, targetURL string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// 1. Acquire worker pool slot
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// 2. Rate limiting check
	if err := stealthpkg.DefaultDomainRateLimiter.Wait(ctx, targetURL); err != nil {
		slog.Warn("Rate limiter wait warning", "url", targetURL, "error", err)
	}

	// 3. Robots.txt check
	allowed, err := stealthpkg.DefaultRobotsChecker.IsAllowed(ctx, "", targetURL)
	if err == nil && !allowed {
		slog.Warn("robots.txt disallows scraping", "url", targetURL)
	}

	b, err := p.getBrowser()
	if err != nil {
		return "", err
	}

	bodyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pageBrowser := b.Context(bodyCtx)

	page, err := stealth.Page(pageBrowser)
	if err != nil {
		if page != nil {
			_ = page.Close()
		}
		page, err = pageBrowser.Page(proto.TargetCreateTarget{URL: targetURL})
		if err != nil {
			return "", fmt.Errorf("failed to create browser tab page: %w", err)
		}
	}
	defer page.Close()

	// 4. Apply stealth profile
	prof := stealthpkg.GetRandomProfile()
	ApplyProfile(page, prof)

	_ = stealthpkg.HumanDelayCtx(bodyCtx, 200, 600)

	if err := page.Navigate(targetURL); err != nil {
		return "", fmt.Errorf("failed to navigate to %s: %w", targetURL, err)
	}

	_ = page.WaitLoad()
	_ = page.WaitStable(1 * time.Second)

	_ = stealthpkg.HumanDelayCtx(bodyCtx, 300, 700)

	htmlContent, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("failed to get HTML from browser tab: %w", err)
	}

	if htmlContent == "" {
		return "", fmt.Errorf("received empty rendered HTML from %s", targetURL)
	}

	return htmlContent, nil
}

// Close closes the underlying Chromium browser instance and frees resources.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	var closeErr error
	if p.browser != nil {
		closeErr = p.browser.Close()
		p.browser = nil
	}

	return closeErr
}
