package teacher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type mockChatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func newMockLLMServer(t *testing.T, handler func(messages []llm.Message) (string, error)) (*httptest.Server, *llm.Client) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}

		var req struct {
			Model    string        `json:"model"`
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		content, err := handler(req.Messages)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		resp := mockChatResponse{}
		resp.Choices = append(resp.Choices, struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		}{
			Message: struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}{
				Role:    "assistant",
				Content: content,
			},
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))

	client := llm.NewClient(config.ProviderConfig{
		BaseURL: ts.URL,
		APIKey:  "mock-api-key",
		Model:   "mock-model",
	})

	return ts, client
}

func setupTestTeacherStore(t *testing.T) (*store.Store, *Store) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_teacher.db")

	rootStore, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to initialize root store: %v", err)
	}

	teacherStore := NewStoreFromAppStore(rootStore)
	return rootStore, teacherStore
}

func TestClarificationFlow_FullHappyPath(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	var turnCount int32
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		currentTurn := atomic.AddInt32(&turnCount, 1)
		switch currentTurn {
		case 1:
			// Turn 1: Ask learner about programming background
			return `{
				"thought": "I need to know their background level to calibrate explanations.",
				"action": {
					"name": "ask_learner",
					"args": {
						"question": "What is your background with programming languages?",
						"input_kind": "single_select",
						"options": ["C/C++", "Python/JavaScript", "Total Beginner"]
					}
				}
			}`, nil

		case 2:
			// Turn 2: Ask learner about target depth
			return `{
				"thought": "Learner has C/C++ background. Now I need target depth.",
				"action": {
					"name": "ask_learner",
					"args": {
						"question": "What depth would you prefer for this report?",
						"input_kind": "single_select",
						"options": ["overview", "working_understanding", "deep_dive"]
					}
				}
			}`, nil

		case 3:
			// Turn 3: Finalize brief
			return `{
				"thought": "Sufficient context gathered. Finalizing learning brief.",
				"action": {
					"name": "finalize_brief",
					"args": {
						"brief": {
							"topic": "Rust Memory Ownership & Borrow Checker",
							"domain": "Systems Programming",
							"learner_level": "Experienced C/C++ programmer transitioning to Rust",
							"motivation": "Understand how borrow checker guarantees memory safety without GC",
							"depth": "working_understanding",
							"known_reference_points": ["Pointers", "Manual memory management", "RAII"],
							"explicit_scope_in": ["Ownership rules", "Borrowing and Lifetimes", "Move semantics"],
							"explicit_scope_out": ["Async Rust runtime details"],
							"format_preferences": {
								"length": "medium",
								"wants_code_examples": true,
								"wants_diagrams": true
							},
							"assumptions_to_avoid": ["Do not assume familiarity with Rust syntax"]
						}
					}
				}
			}`, nil

		default:
			return "", fmt.Errorf("unexpected LLM invocation turn %d", currentTurn)
		}
	})
	defer server.Close()

	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			MinClarificationRounds: 2,
			MaxClarificationRounds: 5,
			DefaultDepth:           "solid working understanding",
		},
	}

	orch := NewOrchestratorWithStore(client, teacherStore, nil, cfg)

	// Create run
	run, err := teacherStore.CreateRun("I want to master Rust ownership and borrowing")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	// Turn 1 (initial intake)
	res1, err := orch.ClarificationTurn(context.Background(), run.ID, "")
	if err != nil {
		t.Fatalf("Turn 1 failed: %v", err)
	}
	if res1.Status != RunStatusClarifying || res1.Round != 1 || res1.Question == nil {
		t.Fatalf("unexpected turn 1 result: %+v", res1)
	}
	if res1.Question.Text != "What is your background with programming languages?" {
		t.Errorf("unexpected question text: %s", res1.Question.Text)
	}
	if len(res1.Question.Options) != 3 {
		t.Errorf("unexpected question options count: %d", len(res1.Question.Options))
	}

	// Turn 2 (learner answers "C/C++")
	res2, err := orch.ClarificationTurn(context.Background(), run.ID, "C/C++")
	if err != nil {
		t.Fatalf("Turn 2 failed: %v", err)
	}
	if res2.Status != RunStatusClarifying || res2.Round != 2 || res2.Question == nil {
		t.Fatalf("unexpected turn 2 result: %+v", res2)
	}
	if res2.Question.Text != "What depth would you prefer for this report?" {
		t.Errorf("unexpected turn 2 question: %s", res2.Question.Text)
	}

	// Turn 3 (learner answers "working_understanding" -> finalize)
	res3, err := orch.ClarificationTurn(context.Background(), run.ID, "working_understanding")
	if err != nil {
		t.Fatalf("Turn 3 failed: %v", err)
	}
	if res3.Status != RunStatusBriefReady || res3.Brief == nil {
		t.Fatalf("unexpected turn 3 result: %+v", res3)
	}
	if res3.Brief.Topic != "Rust Memory Ownership & Borrow Checker" {
		t.Errorf("unexpected brief topic: %s", res3.Brief.Topic)
	}
	if res3.Brief.Domain != "Systems Programming" {
		t.Errorf("unexpected brief domain: %s", res3.Brief.Domain)
	}

	// Verify persistence in DB
	persistedRun, err := teacherStore.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if persistedRun.Status != RunStatusBriefReady || persistedRun.LearningBrief == nil {
		t.Fatalf("persisted run not brief_ready: %+v", persistedRun)
	}
	if persistedRun.LearningBrief.Topic != "Rust Memory Ownership & Borrow Checker" {
		t.Errorf("persisted brief topic mismatch: %s", persistedRun.LearningBrief.Topic)
	}

	clarifications, err := teacherStore.GetClarifications(run.ID)
	if err != nil {
		t.Fatalf("GetClarifications failed: %v", err)
	}
	if len(clarifications) != 2 {
		t.Fatalf("expected 2 clarification rounds in DB, got %d", len(clarifications))
	}
	if clarifications[0].Answer != "C/C++" {
		t.Errorf("expected round 1 answer 'C/C++', got %q", clarifications[0].Answer)
	}
	if clarifications[1].Answer != "working_understanding" {
		t.Errorf("expected round 2 answer 'working_understanding', got %q", clarifications[1].Answer)
	}
}

func TestClarificationFlow_ManualOverride(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	var turnCount int32
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		currentTurn := atomic.AddInt32(&turnCount, 1)
		switch currentTurn {
		case 1:
			return `{
				"thought": "Probing learner background.",
				"action": {
					"name": "ask_learner",
					"args": {
						"question": "What is your math background?",
						"input_kind": "single_select",
						"options": ["Basic Algebra", "Linear Algebra", "Calculus"]
					}
				}
			}`, nil

		case 2:
			// Model sees __start_now__ override instruction and finalizes immediately
			return `{
				"thought": "Learner requested __start_now__. Finalizing immediately with defaults.",
				"action": {
					"name": "finalize_brief",
					"args": {
						"brief": {
							"topic": "Quantum Computing Fundamentals",
							"domain": "Physics and Computation",
							"learner_level": "General beginner",
							"motivation": "Curiosity",
							"depth": "working_understanding",
							"known_reference_points": [],
							"explicit_scope_in": ["Qubits", "Superposition", "Entanglement"],
							"explicit_scope_out": [],
							"format_preferences": {
								"length": "medium",
								"wants_diagrams": true
							},
							"assumptions_to_avoid": []
						}
					}
				}
			}`, nil

		default:
			return "", fmt.Errorf("unexpected turn %d", currentTurn)
		}
	})
	defer server.Close()

	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			MinClarificationRounds: 5, // High min rounds, but manual override should bypass it
			MaxClarificationRounds: 10,
		},
	}

	orch := NewOrchestratorWithStore(client, teacherStore, nil, cfg)

	run, err := teacherStore.CreateRun("Explain quantum computing")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	// Turn 1
	res1, err := orch.ClarificationTurn(context.Background(), run.ID, "")
	if err != nil {
		t.Fatalf("Turn 1 failed: %v", err)
	}
	if res1.Status != RunStatusClarifying || res1.Round != 1 {
		t.Fatalf("unexpected res1: %+v", res1)
	}

	// Turn 2 with __start_now__ manual override
	res2, err := orch.ClarificationTurn(context.Background(), run.ID, ManualOverrideSentinel)
	if err != nil {
		t.Fatalf("Turn 2 failed: %v", err)
	}
	if res2.Status != RunStatusBriefReady || res2.Brief == nil {
		t.Fatalf("expected brief_ready on override, got: %+v", res2)
	}
	if res2.Brief.Topic != "Quantum Computing Fundamentals" {
		t.Errorf("unexpected topic: %s", res2.Brief.Topic)
	}

	persistedRun, err := teacherStore.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if persistedRun.Status != RunStatusBriefReady {
		t.Errorf("expected run status brief_ready, got %s", persistedRun.Status)
	}
}

func TestClarificationFlow_MaxRoundsForcedFinalize(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	var turnCount int32
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		currentTurn := atomic.AddInt32(&turnCount, 1)
		switch currentTurn {
		case 1:
			return `{
				"thought": "Question 1",
				"action": {
					"name": "ask_learner",
					"args": {
						"question": "Question 1?",
						"input_kind": "free_text"
					}
				}
			}`, nil

		case 2:
			return `{
				"thought": "Question 2",
				"action": {
					"name": "ask_learner",
					"args": {
						"question": "Question 2?",
						"input_kind": "free_text"
					}
				}
			}`, nil

		case 3:
			// Max rounds reached (2 rounds max). Model finalizes.
			return `{
				"thought": "Max rounds reached, forced finalization.",
				"action": {
					"name": "finalize_brief",
					"args": {
						"brief": {
							"topic": "Organic Chemistry Basics",
							"domain": "Chemistry",
							"learner_level": "Beginner",
							"motivation": "Exam Prep",
							"depth": "working_understanding",
							"known_reference_points": [],
							"explicit_scope_in": ["Functional groups", "Alkanes"],
							"explicit_scope_out": [],
							"format_preferences": {
								"length": "medium",
								"wants_diagrams": false
							},
							"assumptions_to_avoid": []
						}
					}
				}
			}`, nil

		default:
			return "", fmt.Errorf("unexpected turn %d", currentTurn)
		}
	})
	defer server.Close()

	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			MinClarificationRounds: 1,
			MaxClarificationRounds: 2, // Hard limit 2 rounds
		},
	}

	orch := NewOrchestratorWithStore(client, teacherStore, nil, cfg)

	run, err := teacherStore.CreateRun("Teach me organic chemistry")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	// Turn 1 -> Q1
	res1, err := orch.ClarificationTurn(context.Background(), run.ID, "")
	if err != nil {
		t.Fatalf("Turn 1 failed: %v", err)
	}
	if res1.Status != RunStatusClarifying || res1.Round != 1 {
		t.Fatalf("expected round 1 clarifying, got: %+v", res1)
	}

	// Turn 2 -> Q2
	res2, err := orch.ClarificationTurn(context.Background(), run.ID, "Answer 1")
	if err != nil {
		t.Fatalf("Turn 2 failed: %v", err)
	}
	if res2.Status != RunStatusClarifying || res2.Round != 2 {
		t.Fatalf("expected round 2 clarifying, got: %+v", res2)
	}

	// Turn 3 -> Reached max rounds limit (2 rounds). Forced finalize.
	res3, err := orch.ClarificationTurn(context.Background(), run.ID, "Answer 2")
	if err != nil {
		t.Fatalf("Turn 3 failed: %v", err)
	}
	if res3.Status != RunStatusBriefReady || res3.Brief == nil {
		t.Fatalf("expected brief_ready at max rounds, got: %+v", res3)
	}
	if res3.Brief.Topic != "Organic Chemistry Basics" {
		t.Errorf("unexpected topic: %s", res3.Brief.Topic)
	}
}

func TestClarificationFlow_MinRoundsEnforcement(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	var turnCount int32
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		currentTurn := atomic.AddInt32(&turnCount, 1)
		switch currentTurn {
		case 1:
			// Model prematurely tries to finalize on turn 1
			return `{
				"thought": "I think I know enough already.",
				"action": {
					"name": "finalize_brief",
					"args": {
						"brief": {
							"topic": "Premature Topic",
							"domain": "CS",
							"learner_level": "Beginner"
						}
					}
				}
			}`, nil

		case 2:
			// After being rejected for not meeting min rounds, model asks a question
			lastMsg := messages[len(messages)-1].Content
			if !strings.Contains(lastMsg, "minimum required is 2") {
				return "", fmt.Errorf("expected rejection prompt about min rounds, got: %s", lastMsg)
			}
			return `{
				"thought": "Understood, asking required question.",
				"action": {
					"name": "ask_learner",
					"args": {
						"question": "What is your specific focus area?",
						"input_kind": "free_text"
					}
				}
			}`, nil

		default:
			return "", fmt.Errorf("unexpected turn %d", currentTurn)
		}
	})
	defer server.Close()

	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			MinClarificationRounds: 2,
			MaxClarificationRounds: 5,
		},
	}

	orch := NewOrchestratorWithStore(client, teacherStore, nil, cfg)

	run, err := teacherStore.CreateRun("Learn something")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	// Turn 1 should reject premature finalize and self-correct to ask_learner
	res1, err := orch.ClarificationTurn(context.Background(), run.ID, "")
	if err != nil {
		t.Fatalf("ClarificationTurn failed: %v", err)
	}
	if res1.Status != RunStatusClarifying || res1.Question == nil {
		t.Fatalf("expected clarifying status after rejection, got: %+v", res1)
	}
	if res1.Question.Text != "What is your specific focus area?" {
		t.Errorf("unexpected question text: %s", res1.Question.Text)
	}
}

func TestClarificationFlow_InvalidBriefRejectionAndCorrection(t *testing.T) {
	rootStore, teacherStore := setupTestTeacherStore(t)
	defer rootStore.Close()

	var turnCount int32
	server, client := newMockLLMServer(t, func(messages []llm.Message) (string, error) {
		currentTurn := atomic.AddInt32(&turnCount, 1)
		switch currentTurn {
		case 1:
			// Model returns brief missing required 'domain'
			return `{
				"thought": "Finalizing but invalid brief.",
				"action": {
					"name": "finalize_brief",
					"args": {
						"brief": {
							"topic": "Valid Topic",
							"domain": "",
							"learner_level": "Beginner"
						}
					}
				}
			}`, nil

		case 2:
			// Model receives schema validation error and returns valid brief
			lastMsg := messages[len(messages)-1].Content
			if !strings.Contains(lastMsg, "domain is required") {
				return "", fmt.Errorf("expected domain required error, got: %s", lastMsg)
			}
			return `{
				"thought": "Fixing missing domain field.",
				"action": {
					"name": "finalize_brief",
					"args": {
						"brief": {
							"topic": "Valid Topic",
							"domain": "Computer Science",
							"learner_level": "Beginner",
							"depth": "overview"
						}
					}
				}
			}`, nil

		default:
			return "", fmt.Errorf("unexpected turn %d", currentTurn)
		}
	})
	defer server.Close()

	cfg := &config.Config{
		Teacher: &config.TeacherConfig{
			MinClarificationRounds: 0, // Allow immediate finalize for this test
			MaxClarificationRounds: 5,
		},
	}

	orch := NewOrchestratorWithStore(client, teacherStore, nil, cfg)

	run, err := teacherStore.CreateRun("Learn valid topic")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	res, err := orch.ClarificationTurn(context.Background(), run.ID, ManualOverrideSentinel)
	if err != nil {
		t.Fatalf("ClarificationTurn failed: %v", err)
	}
	if res.Status != RunStatusBriefReady || res.Brief == nil {
		t.Fatalf("expected brief_ready after correction, got: %+v", res)
	}
	if res.Brief.Domain != "Computer Science" {
		t.Errorf("expected domain 'Computer Science', got %q", res.Brief.Domain)
	}
}
