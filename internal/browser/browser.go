package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// FetchRendered launches a stealth Chromium page using go-rod, navigates to targetURL,
// waits for dynamic JS rendering to settle, and returns the rendered HTML.
func FetchRendered(targetURL string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
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

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	browser = browser.Context(ctx)

	page, err := stealth.Page(browser)
	if err != nil {
		page, err = browser.Page(proto.TargetCreateTarget{URL: targetURL})
		if err != nil {
			return "", fmt.Errorf("failed to create page: %w", err)
		}
	}

	defer page.Close()

	// Set realistic viewport (1920x1080)
	_ = (proto.EmulationSetDeviceMetricsOverride{
		Width:             1920,
		Height:            1080,
		DeviceScaleFactor: 1,
		Mobile:            false,
	}).Call(page)

	// Navigate to target URL
	if err := page.Navigate(targetURL); err != nil {
		return "", fmt.Errorf("failed to navigate to %s: %w", targetURL, err)
	}

	// Wait for DOM load & network stability
	_ = page.WaitLoad()
	_ = page.WaitStable(1 * time.Second)

	htmlContent, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("failed to get HTML from page: %w", err)
	}

	if htmlContent == "" {
		return "", fmt.Errorf("received empty rendered HTML from %s", targetURL)
	}

	return htmlContent, nil
}
