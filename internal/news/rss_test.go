package news

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuildGoogleNewsQuery(t *testing.T) {
	tests := []struct {
		name      string
		keywords  string
		when      string
		wantQuery string
	}{
		{
			name:      "single keyword without when",
			keywords:  "AI",
			when:      "",
			wantQuery: "AI",
		},
		{
			name:      "single multi-word keyword with when",
			keywords:  "machine learning",
			when:      "1d",
			wantQuery: `"machine learning" when:1d`,
		},
		{
			name:      "multiple keywords with when prefix already present",
			keywords:  "AI, machine learning, generative AI",
			when:      "when:7d",
			wantQuery: `(AI OR "machine learning" OR "generative AI") when:7d`,
		},
		{
			name:      "empty keywords",
			keywords:  "  ",
			when:      "1d",
			wantQuery: "when:1d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildGoogleNewsQuery(tt.keywords, tt.when)
			if got != tt.wantQuery {
				t.Errorf("BuildGoogleNewsQuery(%q, %q) = %q; want %q", tt.keywords, tt.when, got, tt.wantQuery)
			}
		})
	}
}

func TestFetchGoogleNewsRSS(t *testing.T) {
	mockXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Google News</title>
    <link>https://news.google.com</link>
    <item>
      <title>OpenAI Releases New Frontier Model - TechCrunch</title>
      <link>https://news.google.com/rss/articles/CBMiM2h0dHBzOi8vdGVjaGNydW5jaC5jb20vMjAyNi8wOC8wMS9vcGVuYWktbmV3LW1vZGVsL9IBAA?oc=5</link>
      <pubDate>Sun, 02 Aug 2026 01:00:00 GMT</pubDate>
      <description>&lt;a href="..." target="_blank"&gt;OpenAI Releases New Frontier Model&lt;/a&gt;&amp;nbsp;&amp;nbsp;&lt;font color="#6f6f6f"&gt;TechCrunch&lt;/font&gt;</description>
      <source url="https://techcrunch.com">TechCrunch</source>
    </item>
    <item>
      <title>Google Announces Gemini 3.5 Upgrade - Reuters</title>
      <link>https://news.google.com/rss/articles/CBMiM2h0dHBzOi8vd3d3LnJldXRlcnMuY29tL3RlY2hub2xvZ3kvZ29vZ2xlLWdlbWluaS0yMDI2L9IBAA?oc=5</link>
      <pubDate>Sat, 01 Aug 2026 18:30:00 GMT</pubDate>
      <description>Google unveils major performance gains for Gemini 3.5 series.</description>
      <source url="https://reuters.com">Reuters</source>
    </item>
  </channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockXML))
	}))
	defer server.Close()

	// Use custom transport that redirects google.com requests to mock server
	transport := &mockRedirectTransport{
		targetURL: server.URL,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}

	articles, err := FetchGoogleNewsRSS(context.Background(), httpClient, "AI", "1d")
	if err != nil {
		t.Fatalf("FetchGoogleNewsRSS failed: %v", err)
	}

	if len(articles) != 2 {
		t.Fatalf("expected 2 articles, got %d", len(articles))
	}

	if articles[0].Title != "OpenAI Releases New Frontier Model - TechCrunch" {
		t.Errorf("unexpected title: %s", articles[0].Title)
	}
	if articles[0].Source != "TechCrunch" {
		t.Errorf("unexpected source: %s", articles[0].Source)
	}
	if articles[1].Source != "Reuters" {
		t.Errorf("unexpected source: %s", articles[1].Source)
	}
}

type mockRedirectTransport struct {
	targetURL string
}

func (m *mockRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, m.targetURL, req.Body)
	if err != nil {
		return nil, err
	}
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestRewriteSnippetLink_SwapsGoogleTrackingURL(t *testing.T) {
	cases := []struct {
		name    string
		snippet string
		oldURL  string
		newURL  string
		want    string
	}{
		{
			name:    "google news tracking link swapped to publisher",
			snippet: "[OpenAI Releases New Frontier Model](https://news.google.com/rss/articles/CBMiM2h0dHBzOi8vdGVjaGNydW5jaC5jb20vMjAyNi8wOC8wMS9vcGVuYWktbmV3LW1vZGVsL9IBAA?oc=5) TechCrunch",
			oldURL:  "https://news.google.com/rss/articles/CBMiM2h0dHBzOi8vdGVjaGNydW5jaC5jb20vMjAyNi8wOC8wMS9vcGVuYWktbmV3LW1vZGVsL9IBAA?oc=5",
			newURL:  "https://techcrunch.com/2026/08/01/openai-new-model/",
			want:    "[OpenAI Releases New Frontier Model](https://techcrunch.com/2026/08/01/openai-new-model/) TechCrunch",
		},
		{
			name:    "no link in snippet — returned unchanged",
			snippet: "Google unveils major performance gains for Gemini 3.5 series.",
			oldURL:  "https://news.google.com/rss/articles/CBMiFoo",
			newURL:  "https://reuters.com/technology/google-gemini-2026/",
			want:    "Google unveils major performance gains for Gemini 3.5 series.",
		},
		{
			name:    "snippet link points elsewhere — returned unchanged",
			snippet: "[Title](https://example.com/different) Some source",
			oldURL:  "https://news.google.com/rss/articles/CBMiFoo",
			newURL:  "https://reuters.com/technology/google-gemini-2026/",
			want:    "[Title](https://example.com/different) Some source",
		},
		{
			name:    "empty inputs — returned unchanged",
			snippet: "[Title](https://example.com)",
			oldURL:  "",
			newURL:  "https://reuters.com/x",
			want:    "[Title](https://example.com)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rewriteSnippetLink(tc.snippet, tc.oldURL, tc.newURL)
			if got != tc.want {
				t.Errorf("rewriteSnippetLink(%q, %q, %q)\n  got:  %q\n  want: %q",
					tc.snippet, tc.oldURL, tc.newURL, got, tc.want)
			}
		})
	}
}
