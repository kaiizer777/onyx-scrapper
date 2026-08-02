package research

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/internal/timecontext"
)

type SubQuestion struct {
	ID       int64
	Question string
	Status   string
	Findings []store.Finding
}

type ResearchPlan struct {
	Goal          string
	SubQuestions  []SubQuestion
	ReportOutline []string
}

type Planner struct {
	client      *llm.Client
	authManager *quality.AuthorityManager
}

func NewPlanner(client *llm.Client, authManager *quality.AuthorityManager) *Planner {
	return &Planner{client: client, authManager: authManager}
}

type planResponse struct {
	SubQuestions  []string `json:"sub_questions"`
	ReportOutline []string `json:"report_outline"`
}

func (p *Planner) Plan(ctx context.Context, goal string) (ResearchPlan, error) {
	currentDateStr := timecontext.Now().Format("January 2, 2006")
	prompt := fmt.Sprintf(`You are an expert lead researcher. Your goal is to plan a deep research report for the following topic:
"%s"

Today's date is %s. Use this as the ground truth for what is current — do not rely on your own training knowledge to guess the date or the current state of fast-changing facts. When building a search query about current/latest/recent information, use the actual current year given above, not a year from memory.

Decompose this goal into 3 to 6 independent sub-questions that can be researched in parallel.
Also, define a report structure (outline sections) for the final synthesis.

Respond ONLY with a JSON object in the following format:
{
  "sub_questions": ["Question 1", "Question 2", ...],
  "report_outline": ["Section 1", "Section 2", ...]
}`, goal, currentDateStr)

	messages := []llm.Message{
		{Role: "system", Content: "You are a planning agent for a deep research system. Respond strictly with JSON."},
		{Role: "user", Content: prompt},
	}

	respStr, err := p.client.Chat(ctx, messages)
	if err != nil {
		return ResearchPlan{}, fmt.Errorf("planner chat failed: %w", err)
	}

	cleanResp := strings.TrimSpace(respStr)
	cleanResp = strings.TrimPrefix(cleanResp, "```json")
	cleanResp = strings.TrimPrefix(cleanResp, "```")
	cleanResp = strings.TrimSuffix(cleanResp, "```")
	cleanResp = strings.TrimSpace(cleanResp)

	var pr planResponse
	if err := json.Unmarshal([]byte(cleanResp), &pr); err != nil {
		return ResearchPlan{}, fmt.Errorf("failed to parse planner JSON: %w (raw: %s)", err, cleanResp)
	}

	var sqs []SubQuestion
	for _, q := range pr.SubQuestions {
		sqs = append(sqs, SubQuestion{Question: q})
	}

	return ResearchPlan{
		Goal:          goal,
		SubQuestions:  sqs,
		ReportOutline: pr.ReportOutline,
	}, nil
}

type reflectionResponse struct {
	NewQuestions []string `json:"new_questions"`
}

func (p *Planner) ReflectAndReplan(ctx context.Context, plan ResearchPlan, allFindings []store.Finding) ([]string, error) {
	findingsText := buildFindingsText(allFindings, p.authManager)

	currentDateStr := timecontext.Now().Format("January 2, 2006")
	prompt := fmt.Sprintf(`You are reviewing the findings for the research goal:
"%s"

Today's date is %s. Use this as the ground truth for what is current — do not rely on your own training knowledge to guess the date or the current state of fast-changing facts.

Current findings:
%s

Review these findings against the goal. Are there any significant gaps?
If yes, generate 1 to 3 NEW targeted sub-questions to fill the gaps.
If the coverage is sufficient, return an empty array.

Respond ONLY with a JSON object in the following format:
{
  "new_questions": ["New Question 1", ...]
}`, plan.Goal, currentDateStr, findingsText)

	messages := []llm.Message{
		{Role: "system", Content: "You are a reflection agent for a deep research system. Respond strictly with JSON."},
		{Role: "user", Content: prompt},
	}

	respStr, err := p.client.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("reflection chat failed: %w", err)
	}

	cleanResp := strings.TrimSpace(respStr)
	cleanResp = strings.TrimPrefix(cleanResp, "```json")
	cleanResp = strings.TrimPrefix(cleanResp, "```")
	cleanResp = strings.TrimSuffix(cleanResp, "```")
	cleanResp = strings.TrimSpace(cleanResp)

	var rr reflectionResponse
	if err := json.Unmarshal([]byte(cleanResp), &rr); err != nil {
		return nil, fmt.Errorf("failed to parse reflection JSON: %w", err)
	}

	return rr.NewQuestions, nil
}

func buildFindingsText(findings []store.Finding, authManager *quality.AuthorityManager) string {
	if len(findings) == 0 {
		return "No findings yet."
	}
	
	if authManager != nil {
		engine := quality.NewCorroborationEngine(authManager)
		formattedFindings := engine.GroupAndLabelFindings(findings)
		var sb strings.Builder
		for i, f := range formattedFindings {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, f))
		}
		return sb.String()
	}

	var sb strings.Builder
	for i, f := range findings {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, f.SourceURL, f.Claim))
	}
	return sb.String()
}
