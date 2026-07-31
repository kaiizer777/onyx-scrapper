package research

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type Synthesizer struct {
	client      *llm.Client
	authManager *quality.AuthorityManager
}

func NewSynthesizer(client *llm.Client, authManager *quality.AuthorityManager) *Synthesizer {
	return &Synthesizer{client: client, authManager: authManager}
}

func (s *Synthesizer) Synthesize(ctx context.Context, plan ResearchPlan, findings []store.Finding) (string, error) {
	findingsText := buildFindingsText(findings, s.authManager)
	outlineText := strings.Join(plan.ReportOutline, "\n- ")

	prompt := fmt.Sprintf(`You are the lead synthesizer for a research report.
Your goal is: "%s"

Here is the required report outline:
- %s

Here are the findings gathered by your research team:
%s

Write the final comprehensive markdown report following the outline. 
IMPORTANT: Every claim you make MUST be inline-cited to its source URL using Markdown links or bracketed numbers. For example: "The library is highly performant [1](https://example.com)." Do not invent facts or citations. Only use the findings provided.

Respond directly with the markdown report content. Do not wrap in JSON.`, plan.Goal, outlineText, findingsText)

	messages := []llm.Message{
		{Role: "system", Content: "You are an expert report synthesizer. You output high-quality, cited markdown."},
		{Role: "user", Content: prompt},
	}

	respStr, err := s.client.Chat(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("synthesis chat failed: %w", err)
	}

	return strings.TrimSpace(respStr), nil
}
