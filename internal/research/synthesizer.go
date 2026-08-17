package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

type Synthesizer struct {
	client      *llm.Client
	authManager *quality.AuthorityManager
}

func NewSynthesizer(client *llm.Client, authManager *quality.AuthorityManager) *Synthesizer {
	return &Synthesizer{client: client, authManager: authManager}
}

// SplitFindingsForSynthesis separates findings into active and excluded (contradicted/unclear) sets.
func SplitFindingsForSynthesis(findings []store.Finding) (active []store.Finding, excluded []store.Finding) {
	for _, f := range findings {
		if f.Status == store.StatusContradicted || f.Status == store.StatusUnclear {
			excluded = append(excluded, f)
		} else {
			active = append(active, f)
		}
	}
	return active, excluded
}

func (s *Synthesizer) Synthesize(ctx context.Context, plan ResearchPlan, findings []store.Finding) (string, error) {
	activeFindings, excludedFindings := SplitFindingsForSynthesis(findings)
	findingsText := buildFindingsText(activeFindings, s.authManager)
	outlineText := strings.Join(plan.ReportOutline, "\n- ")

	currentDateStr := timecontext.Now().Format("January 2, 2006")
	prompt := fmt.Sprintf(`You are the lead synthesizer for a research report.
Your goal is: "%s"

Today's date is %s. Use this as the ground truth for what is current.

Here is the required report outline:
- %s

Here are the findings gathered by your research team:
%s

Write the final comprehensive markdown report following the outline. 
IMPORTANT: Every claim you make MUST be inline-cited to its source URL using Markdown links or bracketed numbers. For example: "The library is highly performant [1](https://example.com)." Do not invent facts or citations. Only use the verified findings provided above.

Respond directly with the markdown report content. Do not wrap in JSON.`, plan.Goal, currentDateStr, outlineText, findingsText)

	messages := []llm.Message{
		{Role: "system", Content: "You are an expert report synthesizer. You output high-quality, cited markdown."},
		{Role: "user", Content: prompt},
	}

	respStr, err := s.client.Chat(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("synthesis chat failed: %w", err)
	}

	report := strings.TrimSpace(respStr)

	if len(excludedFindings) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n### Excluded Findings (Verification & Fact Check)\n")
		for _, ef := range excludedFindings {
			statusLabel := string(ef.Status)
			if statusLabel == "" {
				statusLabel = "excluded"
			}
			note := ef.VerificationNote
			if note != "" {
				sb.WriteString(fmt.Sprintf("- **[%s]** %s *(Source: %s, Note: %s)*\n", strings.ToUpper(statusLabel), ef.Claim, ef.SourceURL, note))
			} else {
				sb.WriteString(fmt.Sprintf("- **[%s]** %s *(Source: %s)*\n", strings.ToUpper(statusLabel), ef.Claim, ef.SourceURL))
			}
		}
		report += sb.String()
	}

	return report, nil
}

