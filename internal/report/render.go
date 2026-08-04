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
	return sb.String()
}
