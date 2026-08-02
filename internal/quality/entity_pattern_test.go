package quality

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

type TestCase struct {
	Claim         string `json:"claim"`
	ShouldTrigger bool   `json:"should_trigger"`
}

func TestEntityDetector(t *testing.T) {
	// Mock time to 2024 to align with static test cases
	mockDate := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
	timecontext.SetOverrideDate(mockDate)
	defer timecontext.ClearOverrideDate()

	data, err := os.ReadFile("../../testdata/quality/entity_patterns_cases.json")
	if err != nil {
		t.Fatalf("Failed to read test cases: %v", err)
	}

	var cases []TestCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("Failed to parse test cases: %v", err)
	}

	detector := NewEntityDetector()

	for _, tc := range cases {
		result := detector.IsFreshnessSensitive(tc.Claim)
		if result != tc.ShouldTrigger {
			t.Errorf("Claim: %q, expected %v, got %v", tc.Claim, tc.ShouldTrigger, result)
		}
	}
}
