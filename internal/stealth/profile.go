package stealth

import (
	"math/rand"
	"sync"
	"time"
)

// Profile defines stealth attributes for a browser/HTTP session.
type Profile struct {
	UserAgent       string
	ViewportWidth   int
	ViewportHeight  int
	SecChUa         string
	SecChUaMobile   string
	SecChUaPlatform string
	AcceptLanguage  string
	Timezone        string
}

var (
	rndMu sync.Mutex
	rnd   = rand.New(rand.NewSource(time.Now().UnixNano()))
)

var defaultProfiles = []Profile{
	{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		ViewportWidth:   1920,
		ViewportHeight:  1080,
		SecChUa:         `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		Timezone:        "America/New_York",
	},
	{
		UserAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
		ViewportWidth:   1440,
		ViewportHeight:  900,
		SecChUa:         `"Chromium";v="123", "Google Chrome";v="123", "Not-A.Brand";v="99"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"macOS"`,
		AcceptLanguage:  "en-US,en;q=0.9,en-GB;q=0.8",
		Timezone:        "America/Los_Angeles",
	},
	{
		UserAgent:       "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
		ViewportWidth:   1536,
		ViewportHeight:  864,
		SecChUa:         `"Chromium";v="122", "Google Chrome";v="122", "Not-A.Brand";v="99"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Linux"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		Timezone:        "Europe/London",
	},
	{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Edg/125.0.0.0",
		ViewportWidth:   1366,
		ViewportHeight:  768,
		SecChUa:         `"Microsoft Edge";v="125", "Chromium";v="125", "Not-A.Brand";v="99"`,
		SecChUaMobile:   "?0",
		SecChUaPlatform: `"Windows"`,
		AcceptLanguage:  "en-US,en;q=0.9,es;q=0.8",
		Timezone:        "America/Chicago",
	},
}

// GetRandomProfile returns a randomized realistic user profile.
func GetRandomProfile() Profile {
	rndMu.Lock()
	idx := rnd.Intn(len(defaultProfiles))
	rndMu.Unlock()
	return defaultProfiles[idx]
}
