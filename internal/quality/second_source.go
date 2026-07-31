package quality

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type VerificationResult string

const (
	ResultConfirmed    VerificationResult = "CONFIRMED"
	ResultContradicted VerificationResult = "CONTRADICTED"
	ResultUnclear      VerificationResult = "UNCLEAR"
)

type SecondSourceVerifier struct {
	client   *llm.Client
	registry *discovery.Registry
	detector *EntityDetector
	store    *store.Store
	budget   *Budget
	ttlHours int
}

func NewSecondSourceVerifier(client *llm.Client, registry *discovery.Registry, st *store.Store, budget *Budget, ttlHours int) *SecondSourceVerifier {
	return &SecondSourceVerifier{
		client:   client,
		registry: registry,
		detector: NewEntityDetector(),
		store:    st,
		budget:   budget,
		ttlHours: ttlHours,
	}
}

// VerifyClaim checks if a claim is freshness-sensitive. If so, it performs a second-source
// lookup and uses the LLM to verify the claim against the new evidence.
// It returns the VerificationResult and optionally the contradicted value.
func (v *SecondSourceVerifier) VerifyClaim(ctx context.Context, claim string) (VerificationResult, string, error) {
	if !v.detector.IsFreshnessSensitive(claim) {
		// Not sensitive, skip check
		return ResultUnclear, "", nil
	}

	entity := v.detector.ExtractEntity(claim)
	versionToken := v.detector.ExtractVersionToken(claim)

	if v.store != nil {
		if res, val, ok := v.store.GetEntityCache(entity, versionToken, v.ttlHours); ok {
			return VerificationResult(res), val, nil
		}
	}

	if v.budget != nil && !v.budget.TryAcquire() {
		return ResultUnclear, "", nil
	}

	query := fmt.Sprintf("%s current latest version", entity)

	// Issue one extra search
	results := v.registry.Search(ctx, query)
	if len(results) == 0 {
		return ResultUnclear, "", nil
	}

	// Fetch top 1-2 snippets/pages for verification
	var evidenceBuilder strings.Builder
	for i, res := range results {
		if i >= 2 { // Cap at top 2 for cost/speed
			break
		}
		pc, err := v.registry.Fetch(ctx, res.URL, discovery.FetchOptions{Timeout: 10 * time.Second})
		if err != nil {
			continue
		}
		// Truncate to first 1000 chars for verification to save tokens
		text := pc.CleanText
		if len(text) > 1000 {
			text = text[:1000]
		}
		evidenceBuilder.WriteString(fmt.Sprintf("Source %d:\n%s\n\n", i+1, text))
	}

	evidence := evidenceBuilder.String()
	if evidence == "" {
		return ResultUnclear, "", nil
	}

	prompt := fmt.Sprintf(`Does this second, independent source confirm, contradict, or fail to address this specific claim about %s?
Claim: "%s"

Evidence:
%s

Answer CONFIRMED, CONTRADICTED, or UNCLEAR with the specific current value if contradicted. Format your response exactly like:
RESULT: [CONFIRMED/CONTRADICTED/UNCLEAR]
VALUE: [the current value if contradicted, else blank]`, entity, claim, evidence)

	messages := []llm.Message{
		{Role: "system", Content: "You are a fact-checking assistant. Follow instructions exactly."},
		{Role: "user", Content: prompt},
	}

	respStr, err := v.client.Chat(ctx, messages)
	if err != nil {
		return ResultUnclear, "", err
	}

	res, val, err := parseVerificationResponse(respStr)
	if err == nil && v.store != nil {
		_ = v.store.SaveEntityCache(entity, versionToken, string(res), val)
	}
	return res, val, err
}

func parseVerificationResponse(resp string) (VerificationResult, string, error) {
	lines := strings.Split(resp, "\n")
	var result VerificationResult = ResultUnclear
	var value string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "RESULT:") {
			resStr := strings.TrimSpace(strings.TrimPrefix(line, "RESULT:"))
			switch resStr {
			case string(ResultConfirmed):
				result = ResultConfirmed
			case string(ResultContradicted):
				result = ResultContradicted
			}
		} else if strings.HasPrefix(line, "VALUE:") {
			value = strings.TrimSpace(strings.TrimPrefix(line, "VALUE:"))
		}
	}

	return result, value, nil
}
