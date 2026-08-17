package quality

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
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
func (v *SecondSourceVerifier) VerifyClaim(ctx context.Context, claim string) (VerificationResult, string, error) {
	detected := v.detector.Detect(claim)
	if detected.Type == EntityUnknown {
		// Not sensitive, skip check
		return ResultUnclear, "", nil
	}
	return v.VerifyClaimWithEntity(ctx, claim, detected)
}

// VerifyClaimWithEntity performs verification using a pre-detected entity.
func (v *SecondSourceVerifier) VerifyClaimWithEntity(ctx context.Context, claim string, detected DetectedEntity) (VerificationResult, string, error) {
	if detected.Type == EntityUnknown {
		return ResultUnclear, "", nil
	}

	entity := detected.Subject
	if entity == "" {
		entity = detected.RawMatch
	}
	if entity == "" {
		entity = claim
	}

	cacheToken := CacheToken(detected, claim)

	if v.store != nil {
		if res, val, ok := v.store.GetEntityCache(entity, detected.Type.String(), cacheToken, v.ttlHours); ok {
			return VerificationResult(res), val, nil
		}
	}

	if v.budget != nil && !v.budget.TryAcquire() {
		return ResultUnclear, "", nil
	}

	query := BuildVerificationQuery(detected)

	// Issue search
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

	currentDateStr := timecontext.Now().Format("January 2, 2006")
	prompt := fmt.Sprintf(`Does this second, independent source confirm, contradict, or fail to address this specific claim about %s?
Claim: "%s"

Today's date is %s. Use this as the ground truth for what is current.

Evidence:
%s

Answer CONFIRMED, CONTRADICTED, or UNCLEAR with the specific current value if contradicted. Format your response exactly like:
RESULT: [CONFIRMED/CONTRADICTED/UNCLEAR]
VALUE: [the current value if contradicted, else blank]`, entity, claim, currentDateStr, evidence)

	messages := []llm.Message{
		{Role: "system", Content: "You are a fact-checking assistant. Follow instructions exactly."},
		{Role: "user", Content: prompt},
	}

	respStr, err := v.client.Chat(ctx, messages)
	if err != nil {
		return ResultUnclear, "", err
	}

	res, val, ok := ParseVerificationResult(respStr)
	if !ok {
		rawPreview := respStr
		if len(rawPreview) > 200 {
			rawPreview = rawPreview[:200]
		}
		slog.Warn("verification result parse fallback", "raw", rawPreview)
	}

	if v.store != nil {
		_ = v.store.SaveEntityCache(entity, detected.Type.String(), cacheToken, string(res), val)
	}
	return res, val, nil
}

var resultPattern = regexp.MustCompile(`(?i)result\s*:?\s*\**\[?\s*(CONFIRMED|CONTRADICTED|UNCLEAR)\s*\]?\**`)
var valuePattern = regexp.MustCompile(`(?i)value\s*:?\s*\**\[?\s*(.*?)\s*\]?\**$`)

// ParseVerificationResult parses the LLM response using robust regex matching with fallback line inspection.
// Returns (result, value, ok) where ok is false if no recognizable result keyword was found.
func ParseVerificationResult(raw string) (VerificationResult, string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ResultUnclear, "", false
	}

	lines := strings.Split(trimmed, "\n")
	var result VerificationResult = ResultUnclear
	var value string
	foundResult := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if m := resultPattern.FindStringSubmatch(line); m != nil {
			switch strings.ToUpper(m[1]) {
			case "CONFIRMED":
				result = ResultConfirmed
				foundResult = true
			case "CONTRADICTED":
				result = ResultContradicted
				foundResult = true
			case "UNCLEAR":
				result = ResultUnclear
				foundResult = true
			}
		}
		if m := valuePattern.FindStringSubmatch(line); m != nil {
			v := strings.TrimSpace(m[1])
			if !strings.EqualFold(v, "the current value if contradicted, else blank") {
				value = v
			}
		}
	}

	if foundResult {
		return result, value, true
	}

	// Fallback 1: Check entire string against result pattern
	if m := resultPattern.FindStringSubmatch(raw); m != nil {
		switch strings.ToUpper(m[1]) {
		case "CONFIRMED":
			return ResultConfirmed, value, true
		case "CONTRADICTED":
			return ResultContradicted, value, true
		case "UNCLEAR":
			return ResultUnclear, value, true
		}
	}

	// Fallback 2: Check all lines for bare keywords
	for _, line := range lines {
		lineUpper := strings.ToUpper(strings.TrimSpace(line))
		if lineUpper == "" {
			continue
		}
		for _, kw := range []string{"CONFIRMED", "CONTRADICTED", "UNCLEAR"} {
			if strings.Contains(lineUpper, kw) {
				switch kw {
				case "CONFIRMED":
					return ResultConfirmed, value, true
				case "CONTRADICTED":
					return ResultContradicted, value, true
				case "UNCLEAR":
					return ResultUnclear, value, true
				}
			}
		}
	}

	return ResultUnclear, "", false
}
