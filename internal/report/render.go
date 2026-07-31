package report

import (
	"fmt"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// RenderReport takes the synthesized LLM report and appends the Verification Summary, Insufficient Data callouts, and Sources Consulted.
func RenderReport(llmReport string, dbSqs []store.ResearchSubQuestion, pages []store.Page, authManager *quality.AuthorityManager) string {
	var sb strings.Builder
	
	// Insufficient Data Callouts in the body
	var insufficient []string
	for _, sq := range dbSqs {
		if sq.Status == "insufficient_data" {
			insufficient = append(insufficient, sq.Question)
		}
	}

	if len(insufficient) > 0 {
		sb.WriteString("> [!WARNING]\n")
		sb.WriteString("> **Insufficient Data**\n")
		sb.WriteString("> The following sub-questions could not be fully answered due to lack of usable sources:\n")
		for _, q := range insufficient {
			sb.WriteString(fmt.Sprintf("> - %s\n", q))
		}
		sb.WriteString("\n\n")
	}

	sb.WriteString(llmReport)
	sb.WriteString("\n\n---\n\n## Verification Summary\n\n")

	// Fetch-Integrity Breakdown
	counts := make(map[string]int)
	for _, p := range pages {
		counts[p.FetchIntegrity]++
	}

	sb.WriteString("### Fetch Integrity Breakdown\n")
	sb.WriteString(fmt.Sprintf("- **OK**: %d\n", counts[string(quality.FetchOK)]))
	sb.WriteString(fmt.Sprintf("- **Empty**: %d\n", counts[string(quality.FetchEmpty)]))
	sb.WriteString(fmt.Sprintf("- **Partial**: %d\n", counts[string(quality.FetchPartial)]))
	sb.WriteString(fmt.Sprintf("- **Blocked**: %d\n", counts[string(quality.FetchBlocked)]))
	sb.WriteString(fmt.Sprintf("- **Timeout**: %d\n", counts[string(quality.FetchTimeout)]))
	sb.WriteString(fmt.Sprintf("- **FallbackRecovered**: %d\n", counts[string(quality.FetchFallbackRecovered)]))
	sb.WriteString("\n")

	// Sources Consulted & Authority Breakdown
	tierCounts := make(map[quality.AuthorityTier]int)

	sb.WriteString("### Sources Consulted\n")
	if len(pages) == 0 {
		sb.WriteString("No sources were consulted.\n")
	} else {
		for _, p := range pages {
			tierStr := "unrated"
			if authManager != nil {
				tier := authManager.GetAuthorityTier(p.URL)
				tierCounts[tier]++
				tierStr = tier.String()
			}
			sb.WriteString(fmt.Sprintf("- [%s](%s) (Integrity: `%s`, Tier: `%s`)\n", p.URL, p.URL, p.FetchIntegrity, tierStr))
		}
	}
	
	if authManager != nil {
		sb.WriteString("\n")
		sb.WriteString(fmt.Sprintf("- Source authority: %d primary, %d established, %d general, %d unrated domains cited\n", 
			tierCounts[quality.TierPrimary], 
			tierCounts[quality.TierEstablished], 
			tierCounts[quality.TierGeneral], 
			tierCounts[quality.TierUnknown]))
	}

	return sb.String()
}
