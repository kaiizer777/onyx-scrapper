package quality

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type CorroborationResult struct {
	Label         string
	HighestTier   AuthorityTier
	TierBreakdown map[AuthorityTier]int
	DomainCount   int
	HasConflict   bool
}

type CorroborationConfig struct {
	JaccardThreshold float64
	Stopwords        map[string]bool
}

// DefaultCorroborationConfig returns the safe default configuration for corroboration.
func DefaultCorroborationConfig() CorroborationConfig {
	return CorroborationConfig{
		JaccardThreshold: 0.6,
		Stopwords: map[string]bool{
			"a":       true,
			"an":      true,
			"the":     true,
			"is":      true,
			"was":     true,
			"are":     true,
			"were":    true,
			"be":      true,
			"been":    true,
			"being":   true,
			"in":      true,
			"to":      true,
			"of":      true,
			"for":     true,
			"at":      true,
			"by":      true,
			"on":      true,
			"with":    true,
			"from":    true,
			"as":      true,
			"and":     true,
			"or":      true,
			"that":    true,
			"this":    true,
			"it":      true,
			"its":     true,
			"reached": true,
			"grew":    true,
			"hit":     true,
			"has":     true,
			"have":    true,
			"had":     true,
		},
	}
}

type CorroborationEngine struct {
	authManager *AuthorityManager
	config      CorroborationConfig
}

func NewCorroborationEngine(authManager *AuthorityManager) *CorroborationEngine {
	return NewCorroborationEngineWithConfig(authManager, DefaultCorroborationConfig())
}

func NewCorroborationEngineWithConfig(authManager *AuthorityManager, cfg CorroborationConfig) *CorroborationEngine {
	if cfg.JaccardThreshold <= 0 {
		cfg.JaccardThreshold = 0.6
	}
	if cfg.Stopwords == nil {
		cfg.Stopwords = DefaultCorroborationConfig().Stopwords
	}
	return &CorroborationEngine{
		authManager: authManager,
		config:      cfg,
	}
}

func (e *CorroborationEngine) SetThreshold(threshold float64) {
	e.config.JaccardThreshold = threshold
}

func (e *CorroborationEngine) SetStopwords(stopwords []string) {
	swMap := make(map[string]bool, len(stopwords))
	for _, sw := range stopwords {
		swMap[strings.ToLower(strings.TrimSpace(sw))] = true
	}
	e.config.Stopwords = swMap
}

// DomainKey parses hostnames using net/url, lowercases and strips leading www.,
// and fails open to the raw input URL if parsing fails.
func DomainKey(rawURL string) string {
	return domainKey(rawURL)
}

func domainKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return rawURL
	}
	return strings.TrimPrefix(host, "www.")
}

var (
	anchorMultiplierPattern = regexp.MustCompile(`(?i)(?:[\$€£¥]|usd|eur|gbp)?\s*(\d+(?:,\d{3})*(?:\.\d+)?|\d+(?:\.\d+)?)\s*(billion|million|trillion|thousand|bn|mn|tn|[bmkt]|percent|percentage)\b`)
	anchorPercentPattern    = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%`)
	anchorCurrencyPattern   = regexp.MustCompile(`(?i)[\$€£¥]\s*(\d+(?:,\d{3})*(?:\.\d+)?|\d+(?:\.\d+)?)`)
	anchorVersionPattern    = regexp.MustCompile(`\bv?(\d+\.\d+(?:\.\d+)*)\b`)
	anchorYearPattern       = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
)

func normalizeUnit(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	switch u {
	case "b", "billion", "billions", "bn":
		return "b"
	case "m", "million", "millions", "mn":
		return "m"
	case "t", "trillion", "trillions", "tn":
		return "t"
	case "k", "thousand", "thousands":
		return "k"
	case "%", "percent", "percentage", "pct":
		return "pct"
	default:
		return u
	}
}

// ExtractAnchors pulls normalized numeric/currency/percentage tokens:
// "$50B" and "$50 billion" -> "50b", "25%" / "25 percent" -> "25pct", "$95,000" -> "95000"
func ExtractAnchors(s string) []string {
	return extractAnchors(s)
}

func extractAnchors(s string) []string {
	var anchors []string
	seen := make(map[string]bool)

	addAnchor := func(a string) {
		a = strings.TrimSpace(strings.ToLower(a))
		if a != "" && !seen[a] {
			seen[a] = true
			anchors = append(anchors, a)
		}
	}

	matches := anchorMultiplierPattern.FindAllStringSubmatch(s, -1)
	for _, m := range matches {
		if len(m) >= 3 {
			num := strings.ReplaceAll(m[1], ",", "")
			unit := normalizeUnit(m[2])
			addAnchor(num + unit)
		}
	}

	pctMatches := anchorPercentPattern.FindAllStringSubmatch(s, -1)
	for _, m := range pctMatches {
		if len(m) >= 2 {
			num := strings.ReplaceAll(m[1], ",", "")
			addAnchor(num + "pct")
		}
	}

	currMatches := anchorCurrencyPattern.FindAllStringSubmatch(s, -1)
	for _, m := range currMatches {
		if len(m) >= 2 {
			num := strings.ReplaceAll(m[1], ",", "")
			hasPrefix := false
			for _, a := range anchors {
				if strings.HasPrefix(a, num) {
					hasPrefix = true
					break
				}
			}
			if !hasPrefix {
				addAnchor(num)
			}
		}
	}

	verMatches := anchorVersionPattern.FindAllStringSubmatch(s, -1)
	for _, m := range verMatches {
		if len(m) >= 2 {
			ver := m[1]
			hasPrefix := false
			for _, a := range anchors {
				if strings.HasPrefix(a, ver) {
					hasPrefix = true
					break
				}
			}
			if !hasPrefix {
				addAnchor(ver)
			}
		}
	}

	yearMatches := anchorYearPattern.FindAllStringSubmatch(s, -1)
	for _, m := range yearMatches {
		if len(m) >= 2 {
			year := m[1]
			hasPrefix := false
			for _, a := range anchors {
				if strings.HasPrefix(a, year) {
					hasPrefix = true
					break
				}
			}
			if !hasPrefix {
				addAnchor(year)
			}
		}
	}

	return anchors
}

func normalizeNumericInText(s string) string {
	res := anchorMultiplierPattern.ReplaceAllStringFunc(s, func(match string) string {
		m := anchorMultiplierPattern.FindStringSubmatch(match)
		if len(m) >= 3 {
			num := strings.ReplaceAll(m[1], ",", "")
			unit := normalizeUnit(m[2])
			return " " + num + unit + " "
		}
		return match
	})

	res = anchorPercentPattern.ReplaceAllStringFunc(res, func(match string) string {
		m := anchorPercentPattern.FindStringSubmatch(match)
		if len(m) >= 2 {
			num := strings.ReplaceAll(m[1], ",", "")
			return " " + num + "pct "
		}
		return match
	})

	res = anchorCurrencyPattern.ReplaceAllStringFunc(res, func(match string) string {
		m := anchorCurrencyPattern.FindStringSubmatch(match)
		if len(m) >= 2 {
			num := strings.ReplaceAll(m[1], ",", "")
			return " " + num + " "
		}
		return match
	})

	return res
}

func stripPunctuation(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return b.String()
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// NormalizeClaimText lowercases, normalizes numbers, strips punctuation, collapses whitespace, and removes stopwords.
func NormalizeClaimText(s string) string {
	return normalizeClaimText(s, DefaultCorroborationConfig().Stopwords)
}

func (e *CorroborationEngine) NormalizeClaimText(s string) string {
	return normalizeClaimText(s, e.config.Stopwords)
}

func normalizeClaimText(s string, stopwords map[string]bool) string {
	s = normalizeNumericInText(s)
	s = strings.ToLower(s)
	s = stripPunctuation(s)
	s = collapseWhitespace(s)

	if stopwords == nil {
		return s
	}

	words := strings.Fields(s)
	var filtered []string
	for _, w := range words {
		if !stopwords[w] {
			filtered = append(filtered, w)
		}
	}
	return strings.Join(filtered, " ")
}

func toTokenSet(words []string) map[string]struct{} {
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}

func baseTokenSet(tokens []string, anchors []string) map[string]struct{} {
	anchorSet := make(map[string]bool, len(anchors))
	for _, a := range anchors {
		anchorSet[a] = true
	}
	set := make(map[string]struct{})
	for _, t := range tokens {
		if !anchorSet[t] {
			set[t] = struct{}{}
		}
	}
	return set
}

func jaccardSimilarity(t1, t2 map[string]struct{}) float64 {
	if len(t1) == 0 && len(t2) == 0 {
		return 1.0
	}
	if len(t1) == 0 || len(t2) == 0 {
		return 0.0
	}
	intersection := 0
	for k := range t1 {
		if _, ok := t2[k]; ok {
			intersection++
		}
	}
	union := len(t1) + len(t2) - intersection
	if union == 0 {
		return 1.0
	}
	return float64(intersection) / float64(union)
}

func anchorsAgree(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	setA := make(map[string]bool, len(a))
	for _, x := range a {
		setA[x] = true
	}
	for _, y := range b {
		if !setA[y] {
			return false
		}
	}
	return true
}

func anchorsDisagree(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return !anchorsAgree(a, b)
}

type claimCluster struct {
	NormalizedText string
	FullTokens     map[string]struct{}
	BaseTokens     map[string]struct{}
	Anchors        []string
	Findings       []store.Finding
	Domains        map[string]struct{}
	HasConflict    bool
}

// DetermineLabel returns the specific confidence label based on domains, tiers, and conflict status.
func (e *CorroborationEngine) DetermineLabel(res CorroborationResult) string {
	var base string
	if res.DomainCount <= 1 {
		if res.HighestTier == TierPrimary {
			base = "(single source, but high-authority)"
		} else {
			base = "(single source — unverified)"
		}
	} else {
		// 2+ domains: corroborated or consensus
		prefix := "corroborated"
		if res.DomainCount >= 3 {
			prefix = "consensus"
		}

		if res.HighestTier == TierPrimary {
			base = fmt.Sprintf("%s (primary)", prefix)
		} else if res.HighestTier == TierUnknown {
			base = fmt.Sprintf("%s (low-tier)", prefix)
		} else {
			base = prefix
		}
	}

	if res.HasConflict {
		return fmt.Sprintf("%s [conflicting-values-detected]", base)
	}
	return base
}

// GroupAndLabelFindings groups similar claims across sources to produce corroborated findings.
func (e *CorroborationEngine) GroupAndLabelFindings(findings []store.Finding) []string {
	if len(findings) == 0 {
		return nil
	}

	var clusters []*claimCluster

	for _, f := range findings {
		normText := normalizeClaimText(f.Claim, e.config.Stopwords)
		anchors := extractAnchors(f.Claim)
		tokens := strings.Fields(normText)
		fullTokens := toTokenSet(tokens)
		baseTokens := baseTokenSet(tokens, anchors)

		matchedIdx := -1
		for i, c := range clusters {
			fullSim := jaccardSimilarity(c.FullTokens, fullTokens)
			baseSim := jaccardSimilarity(c.BaseTokens, baseTokens)
			sim := fullSim
			if baseSim > sim {
				sim = baseSim
			}

			if sim >= e.config.JaccardThreshold && anchorsAgree(c.Anchors, anchors) {
				matchedIdx = i
				break
			}
		}

		dk := domainKey(f.SourceURL)
		if matchedIdx >= 0 {
			clusters[matchedIdx].Findings = append(clusters[matchedIdx].Findings, f)
			clusters[matchedIdx].Domains[dk] = struct{}{}
		} else {
			clusters = append(clusters, &claimCluster{
				NormalizedText: normText,
				FullTokens:     fullTokens,
				BaseTokens:     baseTokens,
				Anchors:        anchors,
				Findings:       []store.Finding{f},
				Domains:        map[string]struct{}{dk: {}},
			})
		}
	}

	// Pairwise check for conflicting values across separate clusters
	for i := 0; i < len(clusters); i++ {
		for j := i + 1; j < len(clusters); j++ {
			c1 := clusters[i]
			c2 := clusters[j]

			baseSim := jaccardSimilarity(c1.BaseTokens, c2.BaseTokens)
			fullSim := jaccardSimilarity(c1.FullTokens, c2.FullTokens)
			sim := baseSim
			if fullSim > sim {
				sim = fullSim
			}

			if sim >= e.config.JaccardThreshold && anchorsDisagree(c1.Anchors, c2.Anchors) {
				c1.HasConflict = true
				c2.HasConflict = true
			}
		}
	}

	var formattedFindings []string

	for _, c := range clusters {
		res := CorroborationResult{
			TierBreakdown: make(map[AuthorityTier]int),
			HasConflict:   c.HasConflict,
		}

		uniqueDomains := make(map[string]bool)
		for _, f := range c.Findings {
			var tier AuthorityTier
			if e.authManager != nil {
				tier = e.authManager.GetAuthorityTier(f.SourceURL)
			}
			dk := domainKey(f.SourceURL)
			if !uniqueDomains[dk] {
				uniqueDomains[dk] = true
				res.DomainCount++
				res.TierBreakdown[tier]++
				if tier > res.HighestTier {
					res.HighestTier = tier
				}
			}
		}

		res.Label = e.DetermineLabel(res)

		// Combine sources deduplicated by exact URL
		var sourceLinks []string
		seenURLs := make(map[string]bool)
		for _, f := range c.Findings {
			if !seenURLs[f.SourceURL] {
				seenURLs[f.SourceURL] = true
				sourceLinks = append(sourceLinks, fmt.Sprintf("[%s]", f.SourceURL))
			}
		}
		sourcesStr := strings.Join(sourceLinks, ", ")

		displayClaim := c.Findings[0].Claim
		formattedFindings = append(formattedFindings, fmt.Sprintf("%s %s (Sources: %s)", displayClaim, res.Label, sourcesStr))
	}

	return formattedFindings
}

