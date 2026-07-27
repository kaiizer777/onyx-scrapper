package browser

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
	stealthpkg "github.com/kaiizer-99/onyx-scrapper/internal/stealth"
)

// FetchRendered launches a stealth Chromium page using go-rod, navigates to targetURL,
// waits for dynamic JS rendering to settle, and returns the rendered HTML.
func FetchRendered(ctx context.Context, targetURL string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// 1. Enforce per-domain rate limiting
	if err := stealthpkg.DefaultDomainRateLimiter.Wait(ctx, targetURL); err != nil {
		slog.Warn("Rate limiter wait warning", "url", targetURL, "error", err)
	}

	// 2. Robots.txt check
	allowed, err := stealthpkg.DefaultRobotsChecker.IsAllowed(ctx, "", targetURL)
	if err == nil && !allowed {
		slog.Warn("robots.txt disallows scraping", "url", targetURL)
	}

	// Launch headless browser (leakless false avoids Windows Defender false-positives on temp helper)
	l := launcher.New().
		Headless(true).
		Leakless(false)

	url, err := l.Launch()
	if err != nil {
		return "", fmt.Errorf("failed to launch chromium: %w", err)
	}

	browser := rod.New().ControlURL(url)
	if err := browser.Connect(); err != nil {
		return "", fmt.Errorf("failed to connect to browser: %w", err)
	}
	defer browser.MustClose()

	bodyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	browser = browser.Context(bodyCtx)

	page, err := stealth.Page(browser)
	if err != nil {
		// stealth.Page may return a partial page object on error — close it to
		// avoid leaking the underlying tab/resource before falling back.
		if page != nil {
			_ = page.Close()
		}
		page, err = browser.Page(proto.TargetCreateTarget{URL: targetURL})
		if err != nil {
			return "", fmt.Errorf("failed to create page: %w", err)
		}
	}

	defer page.Close()

	// 3. Apply randomized stealth profile
	prof := stealthpkg.GetRandomProfile()
	ApplyProfile(page, prof)

	// Human pause before navigation
	_ = stealthpkg.HumanDelayCtx(bodyCtx, 300, 800)

	// Navigate to target URL
	if err := page.Navigate(targetURL); err != nil {
		return "", fmt.Errorf("failed to navigate to %s: %w", targetURL, err)
	}

	// Wait for DOM load & network stability
	_ = page.WaitLoad()
	_ = page.WaitStable(1 * time.Second)

	// Human pause after loading
	_ = stealthpkg.HumanDelayCtx(bodyCtx, 400, 900)

	htmlContent, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("failed to get HTML from page: %w", err)
	}

	if htmlContent == "" {
		return "", fmt.Errorf("received empty rendered HTML from %s", targetURL)
	}

	return htmlContent, nil
}

// ApplyProfile applies a stealth profile (viewport, user agent, client hints, timezone) to a rod Page.
func ApplyProfile(page *rod.Page, prof stealthpkg.Profile) {
	_ = (proto.EmulationSetDeviceMetricsOverride{
		Width:             prof.ViewportWidth,
		Height:            prof.ViewportHeight,
		DeviceScaleFactor: 1,
		Mobile:            false,
	}).Call(page)

	_ = (proto.EmulationSetTimezoneOverride{
		TimezoneID: prof.Timezone,
	}).Call(page)

	_ = page.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent:      prof.UserAgent,
		AcceptLanguage: prof.AcceptLanguage,
	})
}

