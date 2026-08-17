package fixtures

import (
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// BugScenario defines a structured test fixture modeling one of the 8 original pipeline defects.
type BugScenario struct {
	Name        string
	BugID       int
	Description string

	// Bug 1: query_template_mismatch
	Claim                    string
	ExpectedEntityType       quality.EntityType
	ExpectedSubject          string
	ExpectedQueryContains    []string
	ExpectedQueryNotContains []string

	// Bug 2: bracketed_llm_result
	MockLLMResponse  string
	ExpectedResult   quality.VerificationResult
	ExpectedResultOk bool
	ExpectedValue    string

	// Bug 3: cache_key_collision
	Claims                    []string
	ExpectDistinctCacheTokens bool

	// Bug 4: paraphrase_corroboration & Bug 5: same_domain_false_multi
	Findings                 []store.Finding
	ExpectedClusterCount     int
	ExpectedDomainCount      int
	ExpectedLabelContains    string
	ExpectedLabelNotContains []string
	ExpectedConflict         bool

	// Bug 6: low_confidence_leak
	Confidence      float64
	MinConfidence   float64
	ExpectPersisted bool

	// Bug 7: contradicted_reaches_synthesis
	FindingStatus           store.FindingStatus
	ExpectInSynthesisPrompt bool
	ExpectInExcludedSection bool

	// Bug 8: agent_bypasses_pipeline
	SourceURL             string
	VisitedURLs           []string
	AuthorityTier         quality.AuthorityTier
	ExpectPipelineSuccess bool
	ExpectStatus          store.FindingStatus
}

// Scenarios contains the canonical test fixtures covering all 8 original defects.
var Scenarios = []BugScenario{
	{
		Name:               "query_template_mismatch",
		BugID:              1,
		Description:        "Executive and Price entities must generate role/price queries without version suffix",
		Claim:              "Tim Cook is the CEO of Apple",
		ExpectedEntityType: quality.EntityExecutive,
		ExpectedSubject:    "Apple",
		ExpectedQueryContains: []string{
			"who is the current",
			"CEO of Apple",
		},
		ExpectedQueryNotContains: []string{
			"current latest version",
		},
	},
	{
		Name:               "query_template_mismatch_price",
		BugID:              1,
		Description:        "Price entity queries must not contain version suffix",
		Claim:              "Bitcoin current price is $95,000",
		ExpectedEntityType: quality.EntityPrice,
		ExpectedSubject:    "Bitcoin",
		ExpectedQueryContains: []string{
			"Bitcoin",
			"current price today",
		},
		ExpectedQueryNotContains: []string{
			"current latest version",
		},
	},
	{
		Name:             "bracketed_llm_result",
		BugID:            2,
		Description:      "Robust parsing of bracketed or bold LLM verification outputs",
		MockLLMResponse:  "RESULT: [CONFIRMED]\nVALUE: []",
		ExpectedResult:   quality.ResultConfirmed,
		ExpectedResultOk: true,
	},
	{
		Name:             "bracketed_llm_result_bold_contradicted",
		BugID:            2,
		Description:      "Parsing markdown bold formatted contradicted verification response",
		MockLLMResponse:  "**RESULT:** [CONTRADICTED]\n**VALUE:** $120,000",
		ExpectedResult:   quality.ResultContradicted,
		ExpectedResultOk: true,
		ExpectedValue:    "$120,000",
	},
	{
		Name:        "cache_key_collision",
		BugID:       3,
		Description: "Distinct executive/claim entities produce unique cache tokens",
		Claims: []string{
			"CEO of Apple",
			"CEO of Google",
			"CEO of Microsoft",
		},
		ExpectDistinctCacheTokens: true,
	},
	{
		Name:        "paraphrase_corroboration",
		BugID:       4,
		Description: "Near-duplicate phrasing across distinct domains merges into one cluster with DomainCount == 2",
		Findings: []store.Finding{
			{
				Claim:     "Q4 revenue reached $50B",
				SourceURL: "https://www.reuters.com/business/article-1",
			},
			{
				Claim:     "In Q4, revenue grew to $50 billion",
				SourceURL: "https://bloomberg.com/news/article-2",
			},
		},
		ExpectedClusterCount:  1,
		ExpectedDomainCount:   2,
		ExpectedLabelContains: "corroborated",
	},
	{
		Name:        "same_domain_false_multi",
		BugID:       5,
		Description: "Multiple URLs from the same host count as single domain and do not get multi-source corroborated label",
		Findings: []store.Finding{
			{
				Claim:     "Nvidia announced new Blackwell GPUs",
				SourceURL: "https://www.techcrunch.com/2024/03/blackwell-launch",
			},
			{
				Claim:     "Nvidia announced new Blackwell GPUs",
				SourceURL: "https://techcrunch.com/2024/03/blackwell-specs-analysis",
			},
		},
		ExpectedClusterCount: 1,
		ExpectedDomainCount:  1,
		ExpectedLabelNotContains: []string{
			"corroborated",
			"consensus",
		},
	},
	{
		Name:            "low_confidence_leak",
		BugID:           6,
		Description:     "Claims below minimum confidence threshold must be dropped before persistence",
		Claim:           "Unverified rumor about unannounced product",
		Confidence:      0.15,
		MinConfidence:   0.40,
		ExpectPersisted: false,
	},
	{
		Name:                    "contradicted_reaches_synthesis",
		BugID:                   7,
		Description:             "Contradicted findings must be excluded from narrative prompt and placed in excluded section",
		Claim:                   "Sam Altman was fired and never returned as OpenAI CEO",
		FindingStatus:           store.StatusContradicted,
		ExpectInSynthesisPrompt: false,
		ExpectInExcludedSection: true,
	},
	{
		Name:                  "agent_bypasses_pipeline",
		BugID:                 8,
		Description:           "Agent record_finding executes URL grounding, confidence filter, second source check, and tiering",
		Claim:                 "CEO of Apple is Tim Cook",
		SourceURL:             "https://apple.com/leadership/tim-cook",
		VisitedURLs:           []string{"https://apple.com/leadership/tim-cook"},
		AuthorityTier:         quality.TierPrimary,
		ExpectPipelineSuccess: true,
		ExpectStatus:          store.StatusActive,
	},
}

// GetScenario retrieves a scenario by name.
func GetScenario(name string) (BugScenario, bool) {
	for _, sc := range Scenarios {
		if sc.Name == name {
			return sc, true
		}
	}
	return BugScenario{}, false
}
