package quality

import (
	"fmt"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type CorroborationResult struct {
	Label         string
	HighestTier   AuthorityTier
	TierBreakdown map[AuthorityTier]int
	DomainCount   int
}

type CorroborationEngine struct {
	authManager *AuthorityManager
}

func NewCorroborationEngine(authManager *AuthorityManager) *CorroborationEngine {
	return &CorroborationEngine{
		authManager: authManager,
	}
}

// DetermineLabel returns the specific confidence label based on domains and tiers
func (e *CorroborationEngine) DetermineLabel(res CorroborationResult) string {
	if res.DomainCount == 1 {
		if res.HighestTier == TierPrimary {
			return "(single source, but high-authority)"
		}
		return "(single source — unverified)"
	}

	// 2+ domains: corroborated or consensus
	prefix := "corroborated"
	if res.DomainCount >= 3 {
		prefix = "consensus"
	}

	if res.HighestTier == TierPrimary {
		return fmt.Sprintf("%s (primary)", prefix)
	}

	// If all are TierUnknown (HighestTier == TierUnknown)
	if res.HighestTier == TierUnknown {
		return fmt.Sprintf("%s (low-tier)", prefix)
	}

	return prefix
}

// GroupAndLabelFindings groups exact/similar claims across sources to produce corroborated findings
func (e *CorroborationEngine) GroupAndLabelFindings(findings []store.Finding) []string {
	// Simple grouping based on exact string match (case-insensitive trim)
	// For production, this could use semantic similarity via LLM embeddings.
	groups := make(map[string][]store.Finding)
	for _, f := range findings {
		key := strings.ToLower(strings.TrimSpace(f.Claim))
		groups[key] = append(groups[key], f)
	}

	var formattedFindings []string
	
	for _, group := range groups {
		res := CorroborationResult{
			TierBreakdown: make(map[AuthorityTier]int),
		}

		uniqueDomains := make(map[string]bool)
		for _, f := range group {
			tier := e.authManager.GetAuthorityTier(f.SourceURL)
			// Deduplicate by URL/Domain for corroboration counting
			domainKey := f.SourceURL // simplified for now
			if !uniqueDomains[domainKey] {
				uniqueDomains[domainKey] = true
				res.DomainCount++
				res.TierBreakdown[tier]++
				if tier > res.HighestTier {
					res.HighestTier = tier
				}
			}
		}

		res.Label = e.DetermineLabel(res)

		// Combine sources
		var sourceLinks []string
		for _, f := range group {
			sourceLinks = append(sourceLinks, fmt.Sprintf("[%s]", f.SourceURL))
		}
		sourcesStr := strings.Join(sourceLinks, ", ")

		// Pick the most complete claim representation
		displayClaim := group[0].Claim
		
		formattedFindings = append(formattedFindings, fmt.Sprintf("%s %s (Sources: %s)", displayClaim, res.Label, sourcesStr))
	}

	return formattedFindings
}
