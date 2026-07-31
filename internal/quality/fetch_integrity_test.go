package quality

import (
	"errors"
	"testing"
)

func TestAnalyzeFetchIntegrity(t *testing.T) {
	tests := []struct {
		name      string
		rawHTML   string
		cleanText string
		provider  string
		fetchErr  error
		expected  FetchIntegrity
	}{
		{
			name:      "Timeout Error",
			rawHTML:   "",
			cleanText: "",
			provider:  "colly",
			fetchErr:  errors.New("context canceled by timeout"),
			expected:  FetchTimeout,
		},
		{
			name:      "Blocked by Error",
			rawHTML:   "",
			cleanText: "",
			provider:  "rod",
			fetchErr:  errors.New("connection refused"),
			expected:  FetchBlocked,
		},
		{
			name:      "Cloudflare Block",
			rawHTML:   "<html><body>cloudflare verify you are human</body></html>",
			cleanText: "verify",
			provider:  "colly",
			fetchErr:  nil,
			expected:  FetchBlocked,
		},
		{
			name:      "Empty Content",
			rawHTML:   "<html><body></body></html>",
			cleanText: "short",
			provider:  "colly",
			fetchErr:  nil,
			expected:  FetchEmpty,
		},
		{
			name:      "Partial HTML (No ending tag)",
			rawHTML:   "<html><body><p>Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times.</p>",
			cleanText: "Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times.",
			provider:  "colly",
			fetchErr:  nil,
			expected:  FetchPartial,
		},
		{
			name:      "JS Required",
			rawHTML:   "<html><body>Please enable Javascript to continue. We need a lot of characters here to pass the minimum content length check so that we can hit the partial check. Here are a lot of characters. We need a lot of characters here to pass the minimum content length check so that we can hit the partial check. Here are a lot of characters.</body></html>",
			cleanText: "Please enable Javascript to continue. We need a lot of characters here to pass the minimum content length check so that we can hit the partial check. Here are a lot of characters. We need a lot of characters here to pass the minimum content length check so that we can hit the partial check. Here are a lot of characters.",
			provider:  "colly",
			fetchErr:  nil,
			expected:  FetchPartial,
		},
		{
			name:      "Fallback Recovered",
			rawHTML:   "<html><body><p>Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times.</p></body></html>",
			cleanText: "Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times.",
			provider:  "scraperapi",
			fetchErr:  nil,
			expected:  FetchFallbackRecovered,
		},
		{
			name:      "OK",
			rawHTML:   "<html><body><p>Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times.</p></body></html>",
			cleanText: "Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times. Lots of content here to pass the minimum character count of two hundred and fifty characters, so we just write a long sentence that repeats a few times.",
			provider:  "colly",
			fetchErr:  nil,
			expected:  FetchOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AnalyzeFetchIntegrity(tt.rawHTML, tt.cleanText, tt.provider, tt.fetchErr)
			if got != tt.expected {
				t.Errorf("AnalyzeFetchIntegrity() = %v, want %v", got, tt.expected)
			}
		})
	}
}
