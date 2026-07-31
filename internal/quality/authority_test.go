package quality

import (
	"os"
	"testing"
)

func TestAuthorityManager(t *testing.T) {
	// Create a temporary yaml config
	yamlData := `primary:
  - ".gov"
  - "arxiv.org"
established:
  - "techcrunch.com"
general:
  - "reddit.com"
`
	tmpFile, err := os.CreateTemp("", "authority_tiers_*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte(yamlData)); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	tmpFile.Close()

	am := NewAuthorityManager()
	if err := am.LoadTiers(tmpFile.Name()); err != nil {
		t.Fatalf("failed to load tiers: %v", err)
	}

	tests := []struct {
		url      string
		expected AuthorityTier
	}{
		{"https://whitehouse.gov/news", TierPrimary},
		{"http://www.arxiv.org/abs/2103.00020", TierPrimary},
		{"https://techcrunch.com/2026/01/01/ai", TierEstablished},
		{"https://www.reddit.com/r/golang", TierGeneral},
		{"https://unknown-blog.com/post", TierUnknown},
		{"https://subdomain.arxiv.org/test", TierPrimary},
	}

	for _, tc := range tests {
		tier := am.GetAuthorityTier(tc.url)
		if tier != tc.expected {
			t.Errorf("url %s: expected %v, got %v", tc.url, tc.expected, tier)
		}
	}
}
