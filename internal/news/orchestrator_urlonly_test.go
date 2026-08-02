package news

import "testing"

func TestLooksLikeMarkdownLinkOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "empty",
			in:   "",
			want: true,
		},
		{
			name: "only a google news rss link",
			in:   "[Popular CT bank](https://news.google.com/rss/articles/CBMipAFBVV95cUxQY2dvU2NqN0FSWHNqNGlYRlNlWWhSUEVZVEtyNUtiZ0o1OEYyb0U0NEpVc001d3ZxS3RtbUlBbFZ4ZF92eFFKYzBmQnJnaFBSN1lQTEI0dGlsUHNVWDZWcWFscEpkYUZlUFJ2UlFqUXBWQ2Y3ZEE0OFhIa1BfUld0T1Ytb2FJdlBFUmMwUmR0VUxhMm5OOFdMaHc4YkdxS2pRLW1kYw) Hartford Courant",
			want: true,
		},
		{
			name: "link with longer trailing text but still under 80",
			in:   "[Title](https://example.com/x) Some short publisher suffix here",
			want: true,
		},
		{
			name: "real prose",
			in:   "Apple announced a new chip on Monday. The new processor delivers 30% more performance per watt than its predecessor. Industry analysts called the move a significant step forward in the on-device AI market.",
			want: false,
		},
		{
			name: "link followed by long prose",
			in:   "[Title](https://example.com/x) Apple announced a new chip on Monday. The new processor delivers 30% more performance per watt than its predecessor. Industry analysts called the move a significant step forward.",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeMarkdownLinkOnly(tc.in)
			if got != tc.want {
				t.Errorf("looksLikeMarkdownLinkOnly(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
