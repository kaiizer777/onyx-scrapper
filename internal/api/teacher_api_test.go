package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
	"github.com/kaiizer777/onyx-scrapper/internal/teacher"
)

// setupMockTeacherServer creates a test server with mocked LLM responses.
func setupMockTeacherServer(t *testing.T, chatHandler http.HandlerFunc) (*httptest.Server, *Server, *store.Store) {
	dbPath := filepath.Join(t.TempDir(), "test_teacher_api.db")
	st, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	mockLLMServer := httptest.NewServer(chatHandler)

	cfg := &config.Config{
		ActiveProvider: "custom",
		Providers: map[string]config.ProviderConfig{
			"custom": {
				BaseURL: mockLLMServer.URL,
				Model:   "mock-teacher-model",
				APIKey:  "test-key",
			},
		},
		Teacher: &config.TeacherConfig{
			MinClarificationRounds: 1,
			MaxClarificationRounds: 5,
		},
	}

	llmClient := llm.NewClient(cfg.ActiveProviderConfig())
	registry := discovery.NewRegistry(nil, nil, nil, nil)
	teacherOrch := teacher.NewOrchestrator(llmClient, st, registry, cfg)

	srv := NewServer(
		WithStore(st),
		WithLLMClient(llmClient),
		WithRegistry(registry),
		WithTeacherOrchestrator(teacherOrch),
	)

	ts := httptest.NewServer(srv.httpSrv.Handler)
	return ts, srv, st
}

func TestTeacherAPI_StartAndClarification(t *testing.T) {
	turn := 0
	chatHandler := func(w http.ResponseWriter, r *http.Request) {
		turn++
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			// Round 1 question
			resp := map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"message": map[string]interface{}{
							"role": "assistant",
							"content": `{"thought": "Asking experience", "action": {"name": "ask_learner", "args": {"question": "What is your background?", "input_kind": "single_select", "options": ["Beginner", "Intermediate", "Advanced"]}}}`,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		} else {
			// Finalize brief
			resp := map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"message": map[string]interface{}{
							"role": "assistant",
							"content": `{"thought": "Finalizing brief", "action": {"name": "finalize_brief", "args": {"brief": {"topic": "Transformer Attention", "domain": "Machine Learning", "learner_level": "intermediate", "motivation": "Understanding LLMs", "depth": "working_understanding", "known_reference_points": ["Neural Networks"], "explicit_scope_in": ["Self-Attention", "Multi-Head Attention"], "explicit_scope_out": ["Training code"], "format_preferences": {"length": "medium", "wants_diagrams": true}, "assumptions_to_avoid": ["Do not assume PhD in math"]}}}}`,
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}

	ts, _, st := setupMockTeacherServer(t, chatHandler)
	defer ts.Close()
	defer st.Close()

	// 1. POST /teacher/start
	startBody, _ := json.Marshal(map[string]string{"goal": "Learn Transformer Attention"})
	respStart, err := http.Post(ts.URL+"/teacher/start", "application/json", bytes.NewBuffer(startBody))
	if err != nil {
		t.Fatalf("POST /teacher/start failed: %v", err)
	}
	defer respStart.Body.Close()

	if respStart.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on start, got %d", respStart.StatusCode)
	}

	var startRes teacher.ClarificationResult
	if err := json.NewDecoder(respStart.Body).Decode(&startRes); err != nil {
		t.Fatalf("failed to decode start JSON: %v", err)
	}

	if startRes.RunID == "" || startRes.Status != teacher.RunStatusClarifying || startRes.Question == nil {
		t.Fatalf("unexpected start response: %+v", startRes)
	}
	if startRes.Question.InputKind != teacher.InputKindSingleSelect {
		t.Errorf("expected input_kind 'single_select', got %q", startRes.Question.InputKind)
	}

	runID := startRes.RunID

	// 2. POST /teacher/answer
	answerBody, _ := json.Marshal(map[string]string{
		"run_id": runID,
		"answer": "Intermediate",
	})
	respAnswer, err := http.Post(ts.URL+"/teacher/answer", "application/json", bytes.NewBuffer(answerBody))
	if err != nil {
		t.Fatalf("POST /teacher/answer failed: %v", err)
	}
	defer respAnswer.Body.Close()

	if respAnswer.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on answer, got %d", respAnswer.StatusCode)
	}

	var answerRes teacher.ClarificationResult
	if err := json.NewDecoder(respAnswer.Body).Decode(&answerRes); err != nil {
		t.Fatalf("failed to decode answer JSON: %v", err)
	}

	if answerRes.Status != teacher.RunStatusBriefReady || answerRes.Brief == nil {
		t.Fatalf("expected brief_ready status with brief, got %+v", answerRes)
	}
	if answerRes.Brief.Topic != "Transformer Attention" {
		t.Errorf("expected brief topic 'Transformer Attention', got %q", answerRes.Brief.Topic)
	}

	// 3. GET /teacher/brief/{run_id}
	respBrief, err := http.Get(fmt.Sprintf("%s/teacher/brief/%s", ts.URL, runID))
	if err != nil {
		t.Fatalf("GET /teacher/brief failed: %v", err)
	}
	defer respBrief.Body.Close()

	var briefPayload struct {
		RunID string                 `json:"run_id"`
		Brief teacher.LearningBrief `json:"brief"`
	}
	if err := json.NewDecoder(respBrief.Body).Decode(&briefPayload); err != nil {
		t.Fatalf("failed to decode brief payload: %v", err)
	}
	if briefPayload.Brief.Topic != "Transformer Attention" {
		t.Errorf("expected brief topic 'Transformer Attention', got %q", briefPayload.Brief.Topic)
	}

	// 4. PATCH /teacher/brief/{run_id}
	patchPayload, _ := json.Marshal(map[string]interface{}{
		"topic":         "Transformer Attention (Deep Dive)",
		"domain":        "Machine Learning",
		"learner_level": "advanced",
		"depth":         "deep_dive",
	})
	reqPatch, _ := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/teacher/brief/%s", ts.URL, runID), bytes.NewBuffer(patchPayload))
	reqPatch.Header.Set("Content-Type", "application/json")
	respPatch, err := http.DefaultClient.Do(reqPatch)
	if err != nil {
		t.Fatalf("PATCH /teacher/brief failed: %v", err)
	}
	defer respPatch.Body.Close()

	if respPatch.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on PATCH, got %d", respPatch.StatusCode)
	}

	var patchedPayload struct {
		RunID string                 `json:"run_id"`
		Brief teacher.LearningBrief `json:"brief"`
	}
	if err := json.NewDecoder(respPatch.Body).Decode(&patchedPayload); err != nil {
		t.Fatalf("failed to decode patched brief JSON: %v", err)
	}
	if patchedPayload.Brief.Topic != "Transformer Attention (Deep Dive)" {
		t.Errorf("expected patched topic, got %q", patchedPayload.Brief.Topic)
	}
}

func TestTeacherAPI_GenerateAndSSEStream(t *testing.T) {
	chatHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `{"sections": [
							{"id": "sec_0", "title": "Core Intuition: Why Attention Matters", "learning_objective": "Understand attention mechanism", "depends_on": []},
							{"id": "sec_1", "title": "Scaled Dot-Product Attention", "learning_objective": "Compute Q, K, V matrices", "depends_on": ["sec_0"]}
						], "verdict": "pass"}`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}

	ts, _, st := setupMockTeacherServer(t, chatHandler)
	defer ts.Close()
	defer st.Close()

	teacherStore := teacher.NewStoreFromAppStore(st)
	run, _ := teacherStore.CreateRun("Explain Attention")
	brief := &teacher.LearningBrief{
		Topic:        "Transformer Attention",
		Domain:       "Computer Science",
		LearnerLevel: "intermediate",
		Depth:        "working_understanding",
	}
	_ = teacherStore.UpdateRunBrief(run.ID, brief)

	// 1. Subscribe to SSE stream before trigger
	streamCtx, streamCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer streamCancel()

	reqStream, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, fmt.Sprintf("%s/teacher/stream/%s", ts.URL, run.ID), nil)
	respStream, err := http.DefaultClient.Do(reqStream)
	if err != nil {
		t.Fatalf("GET /teacher/stream failed: %v", err)
	}
	defer respStream.Body.Close()

	if respStream.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for SSE stream, got %d", respStream.StatusCode)
	}

	// 2. Trigger generation POST /teacher/generate
	genBody, _ := json.Marshal(map[string]string{"run_id": run.ID})
	respGen, err := http.Post(ts.URL+"/teacher/generate", "application/json", bytes.NewBuffer(genBody))
	if err != nil {
		t.Fatalf("POST /teacher/generate failed: %v", err)
	}
	defer respGen.Body.Close()

	if respGen.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on generate, got %d", respGen.StatusCode)
	}

	// 3. Read SSE events and assert ordered lifecycle sequence
	scanner := bufio.NewScanner(respStream.Body)
	var observedEvents []string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			dataJSON := strings.TrimPrefix(line, "data: ")
			var ev teacher.StreamEvent
			if err := json.Unmarshal([]byte(dataJSON), &ev); err == nil {
				observedEvents = append(observedEvents, ev.Event)
				if ev.Event == "done" || ev.Event == "error" {
					break
				}
			}
		}
	}

	if len(observedEvents) == 0 {
		t.Fatalf("did not observe any SSE events on stream")
	}

	// Verify events include outline_ready, drafted, critiquing/done, assembling, done
	hasOutline := false
	hasAssembling := false
	hasDone := false
	for _, ev := range observedEvents {
		if ev == "outline_ready" {
			hasOutline = true
		}
		if ev == "assembling" {
			hasAssembling = true
		}
		if ev == "done" {
			hasDone = true
		}
	}

	if !hasOutline || !hasAssembling || !hasDone {
		t.Errorf("expected sequence to include outline_ready, assembling, done; observed: %v", observedEvents)
	}

	// 4. Test GET /teacher/report/{run_id}
	respReport, err := http.Get(fmt.Sprintf("%s/teacher/report/%s", ts.URL, run.ID))
	if err != nil {
		t.Fatalf("GET /teacher/report failed: %v", err)
	}
	defer respReport.Body.Close()

	var reportData map[string]interface{}
	if err := json.NewDecoder(respReport.Body).Decode(&reportData); err != nil {
		t.Fatalf("failed to decode report JSON: %v", err)
	}
	if reportData["status"] != teacher.RunStatusDone {
		t.Errorf("expected report status 'done', got %v", reportData["status"])
	}
	if reportData["report_md"] == "" {
		t.Errorf("expected non-empty report_md")
	}
}

func TestTeacherAPI_CancelRun(t *testing.T) {
	ts, _, st := setupMockTeacherServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	})
	defer ts.Close()
	defer st.Close()

	teacherStore := teacher.NewStoreFromAppStore(st)
	run, _ := teacherStore.CreateRun("Test Cancel")

	cancelURL := fmt.Sprintf("%s/teacher/runs/%s/cancel", ts.URL, run.ID)
	respCancel, err := http.Post(cancelURL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST cancel failed: %v", err)
	}
	defer respCancel.Body.Close()

	if respCancel.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on cancel, got %d", respCancel.StatusCode)
	}
}

func TestTeacherAPI_RegenerateSection(t *testing.T) {
	chatHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role": "assistant",
						"content": `## Section 1: Updated Core Intuition

This is the regenerated section with fresh examples and deep explanations.

<!-- glossary: self_attention=A mechanism relating different positions of a single sequence -->
`,
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}

	ts, _, st := setupMockTeacherServer(t, chatHandler)
	defer ts.Close()
	defer st.Close()

	teacherStore := teacher.NewStoreFromAppStore(st)
	run, _ := teacherStore.CreateRun("Explain Attention")
	brief := &teacher.LearningBrief{
		Topic:        "Transformer Attention",
		Domain:       "Computer Science",
		LearnerLevel: "intermediate",
		Depth:        "working_understanding",
	}
	_ = teacherStore.UpdateRunBrief(run.ID, brief)

	outlineSec := teacher.TeacherOutlineSection{
		ID:                "to_sec_test",
		RunID:             run.ID,
		SectionOrder:      0,
		Title:             "Core Intuition: Why Attention Matters",
		LearningObjective: "Understand core intuition",
		Status:            teacher.OutlineStatusDone,
	}
	_ = teacherStore.SaveOutline([]teacher.TeacherOutlineSection{outlineSec})

	sec := &teacher.TeacherSection{
		ID:        "ts_sec_test",
		RunID:     run.ID,
		OutlineID: outlineSec.ID,
		DraftMD:   "Old draft",
		FinalMD:   "Old final",
	}
	_ = teacherStore.SaveSectionDraft(sec)

	regenBody, _ := json.Marshal(map[string]string{
		"run_id":     run.ID,
		"section_id": outlineSec.ID,
	})

	respRegen, err := http.Post(fmt.Sprintf("%s/teacher/section/%s/regenerate", ts.URL, outlineSec.ID), "application/json", bytes.NewBuffer(regenBody))
	if err != nil {
		t.Fatalf("POST /teacher/section/.../regenerate failed: %v", err)
	}
	defer respRegen.Body.Close()

	if respRegen.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on regenerate, got %d", respRegen.StatusCode)
	}

	var regenData map[string]interface{}
	if err := json.NewDecoder(respRegen.Body).Decode(&regenData); err != nil {
		t.Fatalf("failed to decode regen response: %v", err)
	}

	if regenData["section_id"] != outlineSec.ID {
		t.Errorf("expected section_id %q, got %v", outlineSec.ID, regenData["section_id"])
	}
	if !strings.Contains(fmt.Sprintf("%v", regenData["final_md"]), "regenerated section") {
		t.Errorf("expected updated final_md, got: %v", regenData["final_md"])
	}
}
