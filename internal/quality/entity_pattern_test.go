package quality

import (
	"encoding/json"
	"os"
	"strings"
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

func TestDetect_PriorityOverExecutiveVsVersion(t *testing.T) {
	detector := NewEntityDetector()
	// Claim contains both executive role and version number
	claim := "CEO of OpenAI announced GPT-5 yesterday"
	detected := detector.Detect(claim)

	if detected.Type != EntityExecutive {
		t.Errorf("Expected EntityExecutive, got %v", detected.Type)
	}
	if detected.Subject != "OpenAI" {
		t.Errorf("Expected Subject 'OpenAI', got %q", detected.Subject)
	}
}

func TestDetect_PriorityPriceOverExecutive(t *testing.T) {
	detector := NewEntityDetector()
	// Claim contains both price and executive
	claim := "Apple CEO announced new pricing of $999 for the device"
	detected := detector.Detect(claim)

	if detected.Type != EntityPrice {
		t.Errorf("Expected EntityPrice, got %v", detected.Type)
	}
}

func TestDetect_PriceEntity(t *testing.T) {
	detector := NewEntityDetector()
	claim := "Bitcoin price $95,000"
	detected := detector.Detect(claim)

	if detected.Type != EntityPrice {
		t.Fatalf("Expected EntityPrice, got %v", detected.Type)
	}
	if detected.Subject != "Bitcoin" {
		t.Errorf("Expected Subject 'Bitcoin', got %q", detected.Subject)
	}
	if !strings.Contains(detected.RawMatch, "$95,000") {
		t.Errorf("Expected RawMatch containing '$95,000', got %q", detected.RawMatch)
	}
}

func TestDetect_ExecutiveEntity(t *testing.T) {
	detector := NewEntityDetector()
	claim := "CEO of Apple is Tim Cook"
	detected := detector.Detect(claim)

	if detected.Type != EntityExecutive {
		t.Fatalf("Expected EntityExecutive, got %v", detected.Type)
	}
	if detected.Subject != "Apple" {
		t.Errorf("Expected Subject 'Apple', got %q", detected.Subject)
	}
}

func TestDetect_VersionEntity(t *testing.T) {
	detector := NewEntityDetector()
	claim := "Claude 3.5 is the latest version of Anthropic's model."
	detected := detector.Detect(claim)

	if detected.Type != EntityVersion {
		t.Fatalf("Expected EntityVersion, got %v", detected.Type)
	}
	if detected.Subject != "Claude" {
		t.Errorf("Expected Subject 'Claude', got %q", detected.Subject)
	}
}

func TestDetect_YearEntity(t *testing.T) {
	mockDate := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
	timecontext.SetOverrideDate(mockDate)
	defer timecontext.ClearOverrideDate()

	detector := NewEntityDetector()
	claim := "As of 2024, the best framework is React."
	detected := detector.Detect(claim)

	if detected.Type != EntityYear {
		t.Fatalf("Expected EntityYear, got %v", detected.Type)
	}
	if detected.RawMatch != "2024" {
		t.Errorf("Expected RawMatch '2024', got %q", detected.RawMatch)
	}
}

func TestBuildVerificationQuery_ExecutiveDoesNotIncludeVersionSuffix(t *testing.T) {
	e := DetectedEntity{
		Type:     EntityExecutive,
		Subject:  "Apple",
		RawMatch: "CEO of Apple",
	}
	query := BuildVerificationQuery(e)

	if strings.Contains(query, "current latest version") {
		t.Errorf("Executive query should not contain 'current latest version': %q", query)
	}
	if query != "who is the current CEO of Apple" {
		t.Errorf("Unexpected query: %q", query)
	}
}

func TestBuildVerificationQuery_PriceDoesNotIncludeVersionSuffix(t *testing.T) {
	e := DetectedEntity{
		Type:     EntityPrice,
		Subject:  "Bitcoin",
		RawMatch: "$95,000",
	}
	query := BuildVerificationQuery(e)

	if strings.Contains(query, "current latest version") {
		t.Errorf("Price query should not contain 'current latest version': %q", query)
	}
	if query != "Bitcoin current price today" {
		t.Errorf("Unexpected query: %q", query)
	}
}

func TestCacheToken_NeverEmpty(t *testing.T) {
	mockDate := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
	timecontext.SetOverrideDate(mockDate)
	defer timecontext.ClearOverrideDate()

	testEntities := []struct {
		name   string
		entity DetectedEntity
		claim  string
	}{
		{
			name:   "Version",
			entity: DetectedEntity{Type: EntityVersion, Subject: "Claude", RawMatch: "Claude 3.5"},
			claim:  "Claude 3.5 is the latest version",
		},
		{
			name:   "Year",
			entity: DetectedEntity{Type: EntityYear, Subject: "React", RawMatch: "2024"},
			claim:  "As of 2024, React is popular",
		},
		{
			name:   "Executive",
			entity: DetectedEntity{Type: EntityExecutive, Subject: "Apple", RawMatch: "CEO of Apple"},
			claim:  "CEO of Apple is Tim Cook",
		},
		{
			name:   "Price",
			entity: DetectedEntity{Type: EntityPrice, Subject: "Bitcoin", RawMatch: "$95,000"},
			claim:  "Bitcoin price $95,000",
		},
		{
			name:   "Unknown",
			entity: DetectedEntity{Type: EntityUnknown, Subject: "", RawMatch: ""},
			claim:  "Generic fact without specific freshness sensitivity",
		},
	}

	for _, tt := range testEntities {
		t.Run(tt.name, func(t *testing.T) {
			token := CacheToken(tt.entity, tt.claim)
			if strings.TrimSpace(token) == "" {
				t.Fatalf("CacheToken returned empty string for entity type %v", tt.entity.Type)
			}
		})
	}
}

func TestCacheToken_DistinctForDistinctExecEntities(t *testing.T) {
	e1 := DetectedEntity{Type: EntityExecutive, Subject: "Apple", RawMatch: "CEO of Apple"}
	e2 := DetectedEntity{Type: EntityExecutive, Subject: "Google", RawMatch: "CEO of Google"}

	t1 := CacheToken(e1, "CEO of Apple")
	t2 := CacheToken(e2, "CEO of Google")

	if t1 == t2 {
		t.Errorf("CacheToken collided for distinct executive entities: %q vs %q", t1, t2)
	}
}
