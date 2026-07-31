package quality

import (
	"os"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

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
			
			// res[0] will look like: "Test claim <label> (Sources: ...)"
			// We can just check if expectedLabel is present
			found := false
			if stringsContains(res[0], tc.expectedLabel) {
				found = true
			}
			if !found {
				t.Errorf("expected string to contain %q, got %q", tc.expectedLabel, res[0])
			}
		})
	}
}

func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || stringIndex(s, substr) >= 0)
}

func stringIndex(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
