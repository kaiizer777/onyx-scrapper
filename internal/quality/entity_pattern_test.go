package quality

import (
	"encoding/json"
	"os"
	"testing"
)

type TestCase struct {
	Claim         string `json:"claim"`
	ShouldTrigger bool   `json:"should_trigger"`
}

func TestEntityDetector(t *testing.T) {
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
