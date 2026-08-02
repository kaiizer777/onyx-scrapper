package news

import (
	"testing"
	"time"
)

func TestParseRecencyWindow(t *testing.T) {
	fixedNow := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name             string
		input            string
		defaultWindowStr string
		wantDuration     time.Duration
		wantWhen         string
		wantRaw          string
	}{
		{
			name:         "shorthand 1h",
			input:        "1h",
			wantDuration: time.Hour,
			wantWhen:     "when:1h",
			wantRaw:      "1h",
		},
		{
			name:         "shorthand 12h",
			input:        "12h",
			wantDuration: 12 * time.Hour,
			wantWhen:     "when:12h",
			wantRaw:      "12h",
		},
		{
			name:         "shorthand 24h",
			input:        "24h",
			wantDuration: 24 * time.Hour,
			wantWhen:     "when:1d",
			wantRaw:      "24h",
		},
		{
			name:         "shorthand 48h",
			input:        "48h",
			wantDuration: 48 * time.Hour,
			wantWhen:     "when:2d",
			wantRaw:      "48h",
		},
		{
			name:         "shorthand 3d",
			input:        "3d",
			wantDuration: 72 * time.Hour,
			wantWhen:     "when:3d",
			wantRaw:      "3d",
		},
		{
			name:         "shorthand 7d",
			input:        "7d",
			wantDuration: 7 * 24 * time.Hour,
			wantWhen:     "when:7d",
			wantRaw:      "7d",
		},
		{
			name:         "shorthand 14d",
			input:        "14d",
			wantDuration: 14 * 24 * time.Hour,
			wantWhen:     "when:14d",
			wantRaw:      "14d",
		},
		{
			name:         "shorthand 1m",
			input:        "1m",
			wantDuration: 30 * 24 * time.Hour,
			wantWhen:     "when:1m",
			wantRaw:      "1m",
		},
		{
			name:         "shorthand 1y",
			input:        "1y",
			wantDuration: 365 * 24 * time.Hour,
			wantWhen:     "when:1y",
			wantRaw:      "1y",
		},
		{
			name:         "phrase last 24 hours",
			input:        "last 24 hours",
			wantDuration: 24 * time.Hour,
			wantWhen:     "when:1d",
			wantRaw:      "last 24 hours",
		},
		{
			name:         "phrase past week",
			input:        "past week",
			wantDuration: 7 * 24 * time.Hour,
			wantWhen:     "when:7d",
			wantRaw:      "past week",
		},
		{
			name:         "keyword today",
			input:        "today",
			wantDuration: 24 * time.Hour,
			wantWhen:     "when:1d",
			wantRaw:      "today",
		},
		{
			name:         "keyword yesterday",
			input:        "yesterday",
			wantDuration: 48 * time.Hour,
			wantWhen:     "when:2d",
			wantRaw:      "yesterday",
		},
		{
			name:         "phrase last 3 days",
			input:        "last 3 days",
			wantDuration: 72 * time.Hour,
			wantWhen:     "when:3d",
			wantRaw:      "last 3 days",
		},
		{
			name:         "phrase this month",
			input:        "this month",
			wantDuration: 30 * 24 * time.Hour,
			wantWhen:     "when:1m",
			wantRaw:      "this month",
		},
		{
			name:         "phrase past 2 weeks",
			input:        "past 2 weeks",
			wantDuration: 14 * 24 * time.Hour,
			wantWhen:     "when:14d",
			wantRaw:      "past 2 weeks",
		},
		{
			name:         "phrase last 12 hours",
			input:        "last 12 hours",
			wantDuration: 12 * time.Hour,
			wantWhen:     "when:12h",
			wantRaw:      "last 12 hours",
		},
		{
			name:         "phrase past 48 hours",
			input:        "past 48 hours",
			wantDuration: 48 * time.Hour,
			wantWhen:     "when:2d",
			wantRaw:      "past 48 hours",
		},
		{
			name:         "phrase 1 hour ago",
			input:        "1 hour ago",
			wantDuration: time.Hour,
			wantWhen:     "when:1h",
			wantRaw:      "1 hour ago",
		},
		{
			name:         "phrase past year",
			input:        "past year",
			wantDuration: 365 * 24 * time.Hour,
			wantWhen:     "when:1y",
			wantRaw:      "past year",
		},
		{
			name:         "embedded phrase in query",
			input:        "give me news from the past 3 days please",
			wantDuration: 72 * time.Hour,
			wantWhen:     "when:3d",
			wantRaw:      "give me news from the past 3 days please",
		},
		{
			name:         "embedded phrase topic in query",
			input:        "what happened in the last 24h about AI",
			wantDuration: 24 * time.Hour,
			wantWhen:     "when:1d",
			wantRaw:      "what happened in the last 24h about AI",
		},
		{
			name:         "unrecognized input falls back to default 24h",
			input:        "xyz garbage query",
			wantDuration: 24 * time.Hour,
			wantWhen:     "when:1d",
			wantRaw:      "xyz garbage query",
		},
		{
			name:         "empty input falls back to default 24h",
			input:        "",
			wantDuration: 24 * time.Hour,
			wantWhen:     "when:1d",
			wantRaw:      DefaultWindow,
		},
		{
			name:             "empty input with custom default window 7d",
			input:            "",
			defaultWindowStr: "7d",
			wantDuration:     7 * 24 * time.Hour,
			wantWhen:         "when:7d",
			wantRaw:          "7d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			win := ParseRecencyWindowAt(tt.input, tt.defaultWindowStr, fixedNow)

			if win.Duration != tt.wantDuration {
				t.Errorf("Duration = %v, want %v", win.Duration, tt.wantDuration)
			}
			if win.GoogleNewsWhen != tt.wantWhen {
				t.Errorf("GoogleNewsWhen = %q, want %q", win.GoogleNewsWhen, tt.wantWhen)
			}
			if win.RawPhrase != tt.wantRaw {
				t.Errorf("RawPhrase = %q, want %q", win.RawPhrase, tt.wantRaw)
			}

			expectedSince := fixedNow.Add(-tt.wantDuration)
			if !win.Since.Equal(expectedSince) {
				t.Errorf("Since = %v, want %v", win.Since, expectedSince)
			}
		})
	}
}
