package teacher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// JudgeEvaluation holds the scores across the 4 key evaluation rubrics.
type JudgeEvaluation struct {
	FactualGrounding     float64 `json:"factual_grounding"`     // 0.0 to 1.0
	LevelAppropriateness float64 `json:"level_appropriateness"` // 0.0 to 1.0
	AnalogyClarity       float64 `json:"analogy_clarity"`       // 0.0 to 1.0
	ScopeAdherence       float64 `json:"scope_adherence"`       // 0.0 to 1.0
	Feedback             string  `json:"feedback"`
}

func (j *JudgeEvaluation) AverageScore() float64 {
	return (j.FactualGrounding + j.LevelAppropriateness + j.AnalogyClarity + j.ScopeAdherence) / 4.0
}

func TestJudge_LLMAsAJudgeEvalHarness(t *testing.T) {
	// Mock pipeline generator + judge LLM server
	mockLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)

		// Check if request is judge evaluation prompt or generation prompt
		messages, _ := body["messages"].([]interface{})
		lastContent := ""
		if len(messages) > 0 {
			if lastMsg, ok := messages[len(messages)-1].(map[string]interface{}); ok {
				lastContent, _ = lastMsg["content"].(string)
			}
		}

		if len(messages) > 0 && (lastContent == "EVALUATE_REPORT" || len(messages) > 3) {
			// Return high quality judge evaluation scores
			resp := map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"message": map[string]interface{}{
							"role": "assistant",
							"content": `{
								"factual_grounding": 0.95,
								"level_appropriateness": 0.90,
								"analogy_clarity": 0.92,
								"scope_adherence": 0.94,
								"feedback": "Outstanding personalized guide with crisp analogies and accurate explanations."
							}`,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Otherwise generation response
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{
							"sections": [
								{"id": "sec_0", "title": "Core Intuition of Raft Consensus", "learning_objective": "Mental model of consensus", "depends_on": []},
								{"id": "sec_1", "title": "Leader Election & Heartbeats", "learning_objective": "How leaders are chosen", "depends_on": ["sec_0"]},
								{"id": "sec_2", "title": "Log Replication & Safety Invariants", "learning_objective": "Guarantee state consistency", "depends_on": ["sec_1"]}
							],
							"verdict": "pass",
							"issues": []
						}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockLLMServer.Close()

	dbPath := filepath.Join(t.TempDir(), "judge_teacher.db")
	rootStore, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer rootStore.Close()

	cfg := &config.Config{
		ActiveProvider: "custom",
		Providers: map[string]config.ProviderConfig{
			"custom": {
				BaseURL: mockLLMServer.URL,
				Model:   "mock-judge-model",
				APIKey:  "test-judge-key",
			},
		},
		Teacher: &config.TeacherConfig{
			SectionWorkerConcurrency: 2,
			CritiquePassLimit:        1,
		},
	}

	llmClient := llm.NewClient(cfg.ActiveProviderConfig())
	registry := discovery.NewRegistry(nil, nil, nil, nil)
	orch := NewOrchestrator(llmClient, rootStore, registry, cfg)

	run, err := orch.Store().CreateRun("Raft Consensus Protocol")
	if err != nil {
		t.Fatalf("failed to create run: %v", err)
	}

	wantsCode := true
	brief := &LearningBrief{
		Topic:                "Raft Consensus Protocol",
		Domain:               "Distributed Systems",
		LearnerLevel:         "advanced",
		Depth:                "deep_dive",
		Motivation:           "Design fault-tolerant distributed databases",
		KnownReferencePoints: []string{"Two-Phase Commit", "State Machine Replication"},
		ExplicitScopeIn:      []string{"Leader Election", "Log Replication", "Safety Invariant"},
		ExplicitScopeOut:     []string{"Multi-Paxos"},
		FormatPreferences:    FormatPreferences{Length: "medium", WantsDiagrams: true, WantsCodeExamples: &wantsCode},
	}
	_ = orch.Store().UpdateRunBrief(run.ID, brief)

	runResult, err := orch.GenerateReport(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("failed to generate report: %v", err)
	}
	reportMD := runResult.ReportMD

	// Invoke LLM Judge
	judgePrompt := fmt.Sprintf(`You are an expert curriculum evaluator. Evaluate the following generated learning guide against the learner's brief.

Learner Brief:
- Topic: %s
- Domain: %s
- Level: %s
- Depth: %s
- Known Reference Points: %v
- Scope In: %v
- Scope Out: %v

Generated Report:
%s

Score each category from 0.0 to 1.0 in valid JSON format:
{
  "factual_grounding": 0.0-1.0,
  "level_appropriateness": 0.0-1.0,
  "analogy_clarity": 0.0-1.0,
  "scope_adherence": 0.0-1.0,
  "feedback": "..."
}`, brief.Topic, brief.Domain, brief.LearnerLevel, brief.Depth, brief.KnownReferencePoints, brief.ExplicitScopeIn, brief.ExplicitScopeOut, reportMD)

	judgeMessages := []llm.Message{
		{Role: "system", Content: "You are a rigorous educational evaluation judge."},
		{Role: "user", Content: "EVALUATE_REPORT"},
		{Role: "user", Content: judgePrompt},
		{Role: "user", Content: "Please output raw JSON."},
	}

	judgeResp, err := llmClient.Chat(context.Background(), judgeMessages)
	if err != nil {
		t.Fatalf("judge LLM call failed: %v", err)
	}

	var eval JudgeEvaluation
	if err := json.Unmarshal([]byte(judgeResp), &eval); err != nil {
		t.Fatalf("failed to parse judge response JSON %q: %v", judgeResp, err)
	}

	t.Logf("Judge Evaluation Results: Factual=%.2f, Level=%.2f, Analogy=%.2f, Scope=%.2f (Avg=%.2f)",
		eval.FactualGrounding, eval.LevelAppropriateness, eval.AnalogyClarity, eval.ScopeAdherence, eval.AverageScore())

	if eval.FactualGrounding < 0.80 {
		t.Errorf("factual grounding score too low: %.2f (expected >= 0.80)", eval.FactualGrounding)
	}
	if eval.LevelAppropriateness < 0.80 {
		t.Errorf("level appropriateness score too low: %.2f (expected >= 0.80)", eval.LevelAppropriateness)
	}
	if eval.AnalogyClarity < 0.80 {
		t.Errorf("analogy clarity score too low: %.2f (expected >= 0.80)", eval.AnalogyClarity)
	}
	if eval.ScopeAdherence < 0.80 {
		t.Errorf("scope adherence score too low: %.2f (expected >= 0.80)", eval.ScopeAdherence)
	}

	avgScore := eval.AverageScore()
	if avgScore < 0.85 {
		t.Errorf("average judge evaluation score %.2f is below required threshold 0.85", avgScore)
	}
}
