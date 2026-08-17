package quality

import (
	"os"
	"strings"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

func TestDomainKey_StripsWWWAndPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://www.techcrunch.com/a", "techcrunch.com"},
		{"https://techcrunch.com/b", "techcrunch.com"},
		{"http://WWW.EXAMPLE.COM:8080/path?query=1", "example.com"},
		{"https://sub.domain.org/foo/bar", "sub.domain.org"},
		{"http://www.whitehouse.gov/briefing", "whitehouse.gov"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := domainKey(tc.input)
			if got != tc.expected {
				t.Errorf("domainKey(%q) = %q, expected %q", tc.input, got, tc.expected)
			}
			gotExported := DomainKey(tc.input)
			if gotExported != tc.expected {
				t.Errorf("DomainKey(%q) = %q, expected %q", tc.input, gotExported, tc.expected)
			}
		})
	}
}

func TestDomainKey_MalformedURLFailsOpen(t *testing.T) {
	tests := []string{
		"://invalid-url",
		"plain-text-not-a-url",
		"",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			got := domainKey(input)
			if got != input {
				t.Errorf("domainKey(%q) = %q, expected %q", input, got, input)
			}
		})
	}
}

func TestNormalizeClaimText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Paraphrase 1",
			input:    "Q4 revenue reached $50B.",
			expected: "q4 revenue 50b",
		},
		{
			name:     "Paraphrase 2",
			input:    "In Q4, revenue grew to $50 billion!",
			expected: "q4 revenue 50b",
		},
		{
			name:     "Currency comma number",
			input:    "Bitcoin price hit $95,000 today",
			expected: "bitcoin price 95000 today",
		},
		{
			name:     "Percentage and year",
			input:    "Growth was 25% in 2024",
			expected: "growth 25pct 2024",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeClaimText(tc.input)
			if got != tc.expected {
				t.Errorf("NormalizeClaimText(%q) = %q, expected %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestExtractAnchors(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Dollar B",
			input:    "$50B",
			expected: []string{"50b"},
		},
		{
			name:     "Dollar billion",
			input:    "$50 billion",
			expected: []string{"50b"},
		},
		{
			name:     "Multiple anchors",
			input:    "Revenue grew from $30B to $50B in 2024",
			expected: []string{"30b", "50b", "2024"},
		},
		{
			name:     "Price with comma",
			input:    "Bitcoin at $95,000",
			expected: []string{"95000"},
		},
		{
			name:     "Percentage",
			input:    "Interest rate 3.5%",
			expected: []string{"3.5pct"},
		},
		{
			name:     "No numbers",
			input:    "Apple CEO visited Paris",
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractAnchors(tc.input)
			if len(got) != len(tc.expected) {
				t.Fatalf("ExtractAnchors(%q) = %v, expected %v", tc.input, got, tc.expected)
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Errorf("ExtractAnchors(%q)[%d] = %q, expected %q", tc.input, i, got[i], tc.expected[i])
				}
			}
		})
	}
}

func TestGroupAndLabel_SameDomainMultipleArticles_CountsAsOneDomain(t *testing.T) {
	authManager := NewAuthorityManager()
	engine := NewCorroborationEngine(authManager)

	findings := []store.Finding{
		{Claim: "Q4 revenue reached $50B", SourceURL: "https://www.techcrunch.com/article1"},
		{Claim: "In Q4, revenue grew to $50 billion", SourceURL: "https://techcrunch.com/article2"},
	}

	res := engine.GroupAndLabelFindings(findings)
	if len(res) != 1 {
		t.Fatalf("expected 1 grouped claim, got %d", len(res))
	}

	// Should count as 1 domain, so label must be single source unverified (not corroborated or consensus)
	if !strings.Contains(res[0], "(single source — unverified)") {
		t.Errorf("expected single source label for same domain, got %q", res[0])
	}
	if strings.Contains(res[0], "corroborated") || strings.Contains(res[0], "consensus") {
		t.Errorf("expected no corroborated/consensus label on single domain, got %q", res[0])
	}
	// Both source URLs should be listed
	if !strings.Contains(res[0], "https://www.techcrunch.com/article1") || !strings.Contains(res[0], "https://techcrunch.com/article2") {
		t.Errorf("expected both sources in formatted output, got %q", res[0])
	}
}

func TestGroupAndLabel_ParaphrasedClaimsAcrossDomains_Merge(t *testing.T) {
	authManager := NewAuthorityManager()
	engine := NewCorroborationEngine(authManager)

	findings := []store.Finding{
		{Claim: "Q4 revenue reached $50B", SourceURL: "https://techcrunch.com/article1"},
		{Claim: "In Q4, revenue grew to $50 billion", SourceURL: "https://reuters.com/article2"},
	}

	res := engine.GroupAndLabelFindings(findings)
	if len(res) != 1 {
		t.Fatalf("expected 1 merged group for paraphrased claims across domains, got %d", len(res))
	}

	if !strings.Contains(res[0], "corroborated") {
		t.Errorf("expected 'corroborated' label for 2 distinct domains, got %q", res[0])
	}
}

func TestGroupAndLabel_DisagreeingNumbers_DoNotMergeSilently(t *testing.T) {
	authManager := NewAuthorityManager()
	engine := NewCorroborationEngine(authManager)

	findings := []store.Finding{
		{Claim: "Q4 revenue reached $50B", SourceURL: "https://techcrunch.com/article1"},
		{Claim: "Q4 revenue reached $30B", SourceURL: "https://reuters.com/article2"},
	}

	res := engine.GroupAndLabelFindings(findings)
	if len(res) != 2 {
		t.Fatalf("expected 2 separate groups for conflicting values, got %d", len(res))
	}

	for i, r := range res {
		if !strings.Contains(r, "conflicting-values-detected") {
			t.Errorf("group %d expected to contain 'conflicting-values-detected', got %q", i, r)
		}
	}
}

func TestGroupAndLabel_UnrelatedClaims_StayUnmerged(t *testing.T) {
	authManager := NewAuthorityManager()
	engine := NewCorroborationEngine(authManager)

	findings := []store.Finding{
		{Claim: "Q4 revenue reached $50B", SourceURL: "https://techcrunch.com/article1"},
		{Claim: "SpaceX launched Starship rocket", SourceURL: "https://space.com/article2"},
	}

	res := engine.GroupAndLabelFindings(findings)
	if len(res) != 2 {
		t.Fatalf("expected 2 distinct groups for unrelated claims, got %d", len(res))
	}

	for i, r := range res {
		if strings.Contains(r, "conflicting-values-detected") {
			t.Errorf("group %d should not have conflict flag, got %q", i, r)
		}
	}
}

func TestGroupAndLabel_ThresholdConfigurable(t *testing.T) {
	authManager := NewAuthorityManager()
	engine := NewCorroborationEngine(authManager)

	// Claims with moderate token overlap (Jaccard ~0.8)
	findings := []store.Finding{
		{Claim: "Apple introduced new generative AI features for iPhone in iOS 18", SourceURL: "https://techcrunch.com/1"},
		{Claim: "Apple announced new generative AI features for iPhone in iOS 18", SourceURL: "https://reuters.com/2"},
	}

	// With default threshold (0.6), they merge
	engine.SetThreshold(0.6)
	resDefault := engine.GroupAndLabelFindings(findings)
	if len(resDefault) != 1 {
		t.Fatalf("expected 1 merged group at threshold 0.6, got %d", len(resDefault))
	}

	// With strict threshold (0.95), they stay separated
	engine.SetThreshold(0.95)
	resStrict := engine.GroupAndLabelFindings(findings)
	if len(resStrict) != 2 {
		t.Fatalf("expected 2 separate groups at threshold 0.95, got %d", len(resStrict))
	}
}

func TestCorroborationEngine(t *testing.T) {
	yamlData := `primary:
  - ".gov"
established:
  - "techcrunch.com"
`
	tmpFile, err := os.CreateTemp("", "authority_tiers_*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Write([]byte(yamlData))
	tmpFile.Close()

	authManager := NewAuthorityManager()
	authManager.LoadTiers(tmpFile.Name())
	engine := NewCorroborationEngine(authManager)

	tests := []struct {
		name          string
		findings      []store.Finding
		expectedLabel string
	}{
		{
			name: "Single source unknown",
			findings: []store.Finding{
				{Claim: "Test claim", SourceURL: "https://unknown.com/1"},
			},
			expectedLabel: "(single source — unverified)",
		},
		{
			name: "Single source primary",
			findings: []store.Finding{
				{Claim: "Test claim", SourceURL: "https://whitehouse.gov/1"},
			},
			expectedLabel: "(single source, but high-authority)",
		},
		{
			name: "Corroborated low tier",
			findings: []store.Finding{
				{Claim: "Test claim", SourceURL: "https://unknown1.com/1"},
				{Claim: "Test claim", SourceURL: "https://unknown2.com/1"},
			},
			expectedLabel: "corroborated (low-tier)",
		},
		{
			name: "Corroborated primary",
			findings: []store.Finding{
				{Claim: "Test claim", SourceURL: "https://unknown1.com/1"},
				{Claim: "Test claim", SourceURL: "https://whitehouse.gov/1"},
			},
			expectedLabel: "corroborated (primary)",
		},
		{
			name: "Consensus established",
			findings: []store.Finding{
				{Claim: "Test claim", SourceURL: "https://unknown1.com/1"},
				{Claim: "Test claim", SourceURL: "https://techcrunch.com/1"},
				{Claim: "Test claim", SourceURL: "https://unknown3.com/1"},
			},
			expectedLabel: "consensus",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := engine.GroupAndLabelFindings(tc.findings)
			if len(res) != 1 {
				t.Fatalf("expected 1 group, got %d", len(res))
			}

			if !strings.Contains(res[0], tc.expectedLabel) {
				t.Errorf("expected string to contain %q, got %q", tc.expectedLabel, res[0])
			}
		})
	}
}

