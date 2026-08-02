package discovery

import (
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

func TestRewriteStaleYearQuery(t *testing.T) {
	// Set the current time to 2026 for consistent testing
	mockDate := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	timecontext.SetOverrideDate(mockDate)
	defer timecontext.ClearOverrideDate()

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "No recency keyword, no rewrite",
			query:    "best AI models 2024",
			expected: "best AI models 2024",
		},
		{
			name:     "Recency keyword with stale year",
			query:    "latest AI models 2024",
			expected: "latest AI models 2026",
		},
		{
			name:     "Recency keyword with current year",
			query:    "latest AI models 2026",
			expected: "latest AI models 2026",
		},
		{
			name:     "Recency keyword with last year (should not rewrite if within 1 year)",
			query:    "latest AI models 2025",
			expected: "latest AI models 2025", // 2026 - 1 = 2025, which is not < 2025
		},
		{
			name:     "Multiple stale years",
			query:    "newest trends in 2023 vs 2024",
			expected: "newest trends in 2026 vs 2026",
		},
		{
			name:     "Different recency keyword",
			query:    "current stock price of AAPL 2021",
			expected: "current stock price of AAPL 2026",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteStaleYearQuery(tt.query)
			if got != tt.expected {
				t.Errorf("rewriteStaleYearQuery(%q) = %q, want %q", tt.query, got, tt.expected)
			}
		})
	}
}
