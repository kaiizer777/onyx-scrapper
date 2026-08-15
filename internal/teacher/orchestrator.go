package teacher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/config"
	discoverypkg "github.com/kaiizer777/onyx-scrapper/internal/discovery"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/quality"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// Orchestrator coordinates the Teacher Agent pipeline (Clarification, Outline Planning, Research, Writing, Critique, Assembly).
type Orchestrator struct {
	client      *llm.Client
	store       *Store
	registry    *discoverypkg.Registry
	cfg         *config.Config
	authManager *quality.AuthorityManager
	broadcaster *EventBroadcaster
}

// NewOrchestrator constructs a new Teacher Orchestrator instance.
func NewOrchestrator(client *llm.Client, appStore *store.Store, registry *discoverypkg.Registry, cfg *config.Config) *Orchestrator {
	var teacherStore *Store
	if appStore != nil {
		teacherStore = NewStoreFromAppStore(appStore)
	}
	return NewOrchestratorWithStore(client, teacherStore, registry, cfg)
}

// NewOrchestratorWithStore constructs an Orchestrator with an explicit Teacher Store.
func NewOrchestratorWithStore(client *llm.Client, teacherStore *Store, registry *discoverypkg.Registry, cfg *config.Config) *Orchestrator {
	var authManager *quality.AuthorityManager
	if cfg != nil && cfg.Quality != nil && (cfg.Quality.SourceAuthority.Enabled == nil || *cfg.Quality.SourceAuthority.Enabled) {
		authManager = quality.NewAuthorityManager()
		tiersPath := cfg.Quality.SourceAuthority.TiersConfigPath
		if tiersPath == "" {
			tiersPath = "config/authority_tiers.yaml"
		}
		if err := authManager.LoadTiers(tiersPath); err != nil {
			slog.Warn("Teacher: failed to load authority tiers", "error", err)
		}
	}

	return &Orchestrator{
		client:      client,
		store:       teacherStore,
		registry:    registry,
		cfg:         cfg,
		authManager: authManager,
		broadcaster: NewEventBroadcaster(),
	}
}

// Broadcaster returns the SSE event broadcaster instance.
func (o *Orchestrator) Broadcaster() *EventBroadcaster {
	if o.broadcaster == nil {
		o.broadcaster = NewEventBroadcaster()
	}
	return o.broadcaster
}

func (o *Orchestrator) emitEvent(runID string, eventName string, data interface{}) {
	if o.broadcaster != nil {
		o.broadcaster.Broadcast(StreamEvent{
			RunID:     runID,
			Event:     eventName,
			Data:      data,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// Store returns the teacher SQLite store.
func (o *Orchestrator) Store() *Store {
	return o.store
}

const (
	ManualOverrideSentinel = "__start_now__"
	MaxClarificationRetries = 3
)

// ClarificationTurn executes one turn of the clarification loop ("The Grill").
// If learnerAnswer is empty (""), this is the initial intake turn for the run.
// If learnerAnswer is "__start_now__", this forces immediate finalization of the brief.
func (o *Orchestrator) ClarificationTurn(ctx context.Context, runID string, learnerAnswer string) (*ClarificationResult, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}
	if o.client == nil {
		return nil, errors.New("llm client is not initialized")
	}

	run, err := o.store.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch run %s: %w", runID, err)
	}
	if run == nil {
		return nil, fmt.Errorf("teacher run %s not found", runID)
	}

	// If the run has already finalized a brief, return it directly.
	if run.Status == RunStatusBriefReady && run.LearningBrief != nil {
		return &ClarificationResult{
			RunID:  runID,
			Status: RunStatusBriefReady,
			Brief:  run.LearningBrief,
		}, nil
	}
	if run.Status == RunStatusError {
		return nil, fmt.Errorf("teacher run is in error state: %s", run.ErrorMessage)
	}

	// Fetch existing clarification rounds.
	rounds, err := o.store.GetClarifications(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get clarifications for run %s: %w", runID, err)
	}

	// Get teacher config bounds.
	var minRounds, maxRounds int
	var defaultDepth string
	if o.cfg != nil {
		tCfg := o.cfg.GetTeacherConfig()
		minRounds = tCfg.MinClarificationRounds
		maxRounds = tCfg.MaxClarificationRounds
		defaultDepth = tCfg.DefaultDepth
	}
	if minRounds <= 0 {
		minRounds = 2
	}
	if maxRounds <= 0 {
		maxRounds = 10
	}
	if defaultDepth == "" {
		defaultDepth = "solid working understanding"
	}

	cleanAnswer := strings.TrimSpace(learnerAnswer)
	isManualOverride := cleanAnswer == ManualOverrideSentinel

	// If there are existing rounds:
	if len(rounds) > 0 {
		lastIdx := len(rounds) - 1
		if cleanAnswer != "" {
			if rounds[lastIdx].Answer == "" {
				if err := o.store.UpdateClarificationAnswer(rounds[lastIdx].ID, cleanAnswer); err != nil {
					return nil, fmt.Errorf("failed to update clarification answer: %w", err)
				}
				rounds[lastIdx].Answer = cleanAnswer
			}
		} else if rounds[lastIdx].Answer == "" {
			// Idempotent: return the existing pending unanswered round without generating extra rounds or re-prompting LLM
			return &ClarificationResult{
				RunID:    runID,
				Status:   RunStatusClarifying,
				Round:    rounds[lastIdx].Round,
				Question: &rounds[lastIdx].Question,
			}, nil
		}
	}

	// Determine if this turn should force finalization due to max rounds limit or override.
	forceFinalize := isManualOverride || (len(rounds) >= maxRounds)

	// Build message history
	systemPrompt := BuildClarificationPrompt(minRounds, maxRounds, defaultDepth)
	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: fmt.Sprintf("I want to learn about: %s", run.RawGoal)},
	}

	// Replay past clarification turns
	for _, r := range rounds {
		argsBytes, _ := json.Marshal(AskLearnerArgs{
			Question:  r.Question.Text,
			InputKind: r.Question.InputKind,
			Options:   r.Question.Options,
		})
		actionResp := TeacherActionResponse{
			Thought: "Gathering clarification context from learner",
		}
		actionResp.Action.Name = ToolAskLearner
		actionResp.Action.Args = argsBytes
		actionJSON, _ := json.Marshal(actionResp)

		messages = append(messages, llm.Message{
			Role:    "assistant",
			Content: string(actionJSON),
		})

		if strings.TrimSpace(r.Answer) != "" {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: fmt.Sprintf("Learner answer: %s", r.Answer),
			})
		}
	}

	if isManualOverride {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: "The learner has requested to start immediately ('__start_now__'). You MUST call finalize_brief now with your best-effort Learning Brief using whatever context has been gathered so far. Use reasonable defaults for unknown fields.",
		})
	} else if len(rounds) >= maxRounds {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: fmt.Sprintf("Maximum clarification rounds reached (%d/%d). You MUST call finalize_brief now with your best-effort Learning Brief, filling unknown fields with reasonable defaults.", len(rounds), maxRounds),
		})
	}

	// Retry loop for LLM execution / parsing / validation
	var lastErr error
	for attempt := 0; attempt < MaxClarificationRetries; attempt++ {
		respStr, err := o.client.Chat(ctx, messages)
		if err != nil {
			return nil, fmt.Errorf("llm chat failed in clarification turn: %w", err)
		}

		cleanResp := cleanActionJSON(respStr)
		var actionResp TeacherActionResponse
		if err := json.Unmarshal([]byte(cleanResp), &actionResp); err != nil {
			lastErr = fmt.Errorf("failed to parse response as JSON: %w (raw: %s)", err, cleanResp)
			messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
			messages = append(messages, llm.Message{Role: "user", Content: fmt.Sprintf("Invalid JSON: %v. Please respond strictly with valid JSON conforming to the schema.", err)})
			continue
		}

		actionName := strings.ToLower(strings.TrimSpace(actionResp.Action.Name))

		switch actionName {
		case ToolAskLearner:
			if forceFinalize {
				// Re-prompt model if it tried to ask another question after being told to finalize
				feedback := "You were instructed to finalize the brief immediately. You cannot ask more questions. Call finalize_brief now."
				messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
				messages = append(messages, llm.Message{Role: "user", Content: feedback})
				continue
			}

			var askArgs AskLearnerArgs
			if err := json.Unmarshal(actionResp.Action.Args, &askArgs); err != nil {
				messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
				messages = append(messages, llm.Message{Role: "user", Content: fmt.Sprintf("Invalid ask_learner args: %v. Provide valid arguments.", err)})
				continue
			}

			qText := askArgs.GetQuestion()
			if qText == "" {
				messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
				messages = append(messages, llm.Message{Role: "user", Content: "The question field cannot be empty. Please provide a clear question."})
				continue
			}

			inputKind := strings.ToLower(strings.TrimSpace(askArgs.InputKind))
			if inputKind == "" {
				inputKind = InputKindFreeText
			}

			nextRoundNum := len(rounds) + 1
			newRound := &ClarificationRound{
				RunID: runID,
				Round: nextRoundNum,
				Question: ClarificationQuestion{
					Text:      qText,
					InputKind: inputKind,
					Options:   askArgs.Options,
				},
				CreatedAt: time.Now().UTC(),
			}

			if err := o.store.SaveClarification(newRound); err != nil {
				return nil, fmt.Errorf("failed to save clarification round: %w", err)
			}

			return &ClarificationResult{
				RunID:    runID,
				Status:   RunStatusClarifying,
				Round:    nextRoundNum,
				Question: &newRound.Question,
			}, nil

		case ToolFinalizeBrief:
			var brief LearningBrief
			var finalizeArgs FinalizeBriefArgs
			if err := json.Unmarshal(actionResp.Action.Args, &finalizeArgs); err == nil && finalizeArgs.Brief.Topic != "" {
				brief = finalizeArgs.Brief
			} else {
				// Fallback if model supplied brief directly in args
				if err := json.Unmarshal(actionResp.Action.Args, &brief); err != nil {
					messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
					messages = append(messages, llm.Message{Role: "user", Content: fmt.Sprintf("Invalid finalize_brief args: %v. Please provide a valid brief object.", err)})
					continue
				}
			}

			// Enforce min_clarification_rounds if not in manual override and not forced by max rounds
			if !isManualOverride && !forceFinalize && len(rounds) < minRounds {
				feedback := fmt.Sprintf("You cannot finalize the brief yet. You have only asked %d questions, but the minimum required is %d. Please ask another clarifying question using ask_learner.", len(rounds), minRounds)
				messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
				messages = append(messages, llm.Message{Role: "user", Content: feedback})
				continue
			}

			if brief.Depth == "" {
				brief.Depth = defaultDepth
			}

			// Validate brief schema
			if err := brief.Validate(); err != nil {
				feedback := fmt.Sprintf("LearningBrief schema validation failed: %v. Please call finalize_brief again with a valid and complete brief.", err)
				messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
				messages = append(messages, llm.Message{Role: "user", Content: feedback})
				continue
			}

			// Persist brief to DB
			if err := o.store.UpdateRunBrief(runID, &brief); err != nil {
				return nil, fmt.Errorf("failed to persist learning brief to store: %w", err)
			}

			return &ClarificationResult{
				RunID:  runID,
				Status: RunStatusBriefReady,
				Round:  len(rounds),
				Brief:  &brief,
			}, nil

		default:
			feedback := fmt.Sprintf("Unknown action %q. You must call either ask_learner or finalize_brief.", actionName)
			messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
			messages = append(messages, llm.Message{Role: "user", Content: feedback})
			continue
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("clarification turn failed after %d retries: %w", MaxClarificationRetries, lastErr)
	}
	return nil, fmt.Errorf("clarification turn exceeded max retries without resolving an action")
}

// GenerateReport coordinates the complete Teacher pipeline:
// 1. GenerateOutline (Phase 5) -> 2. ResearchOutline (Phase 4) -> 3. DraftAllSections (Phase 6) -> 4. CritiqueAllSections (Phase 7) -> 5. AssembleReport (Phase 8)
func (o *Orchestrator) GenerateReport(ctx context.Context, runID string) (*TeacherRun, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}

	start := time.Now()

	// Verify brief exists
	brief, err := o.GetBrief(runID)
	if err != nil {
		o.emitEvent(runID, "error", map[string]string{"error_message": fmt.Sprintf("invalid brief: %v", err)})
		return nil, fmt.Errorf("cannot generate report without validated learning brief: %w", err)
	}
	if brief == nil {
		o.emitEvent(runID, "error", map[string]string{"error_message": "learning brief is nil"})
		return nil, errors.New("learning brief is nil")
	}

	slog.Info("Starting Teacher report generation pipeline", "run_id", runID, "topic", brief.Topic)

	// Step 1: Outline Planning (Phase 5)
	t0 := time.Now()
	outline, err := o.GenerateOutline(ctx, runID)
	if err != nil {
		_ = o.store.UpdateRunError(runID, fmt.Sprintf("Outline generation failed: %v", err))
		o.emitEvent(runID, "error", map[string]string{"error_message": fmt.Sprintf("Outline generation failed: %v", err)})
		return nil, fmt.Errorf("pipeline step 1 (outline) failed: %w", err)
	}
	slog.Info("Teacher outline generated", "run_id", runID, "section_count", len(outline), "duration", time.Since(t0))

	// Step 2: Research Section Claims (Phase 4)
	t1 := time.Now()
	findings, err := o.ResearchOutline(ctx, runID)
	if err != nil {
		_ = o.store.UpdateRunError(runID, fmt.Sprintf("Section research failed: %v", err))
		o.emitEvent(runID, "error", map[string]string{"error_message": fmt.Sprintf("Section research failed: %v", err)})
		return nil, fmt.Errorf("pipeline step 2 (research) failed: %w", err)
	}
	slog.Info("Teacher research completed", "run_id", runID, "findings_count", len(findings), "duration", time.Since(t1))

	// Step 3: Section Drafting (Phase 6)
	t2 := time.Now()
	drafts, err := o.DraftAllSections(ctx, runID)
	if err != nil {
		_ = o.store.UpdateRunError(runID, fmt.Sprintf("Section drafting failed: %v", err))
		o.emitEvent(runID, "error", map[string]string{"error_message": fmt.Sprintf("Section drafting failed: %v", err)})
		return nil, fmt.Errorf("pipeline step 3 (drafting) failed: %w", err)
	}
	slog.Info("Teacher drafts completed", "run_id", runID, "drafts_count", len(drafts), "duration", time.Since(t2))

	// Step 4: Critique & Refinement Loop (Phase 7)
	t3 := time.Now()
	refined, err := o.CritiqueAllSections(ctx, runID)
	if err != nil {
		_ = o.store.UpdateRunError(runID, fmt.Sprintf("Critique loop failed: %v", err))
		o.emitEvent(runID, "error", map[string]string{"error_message": fmt.Sprintf("Critique loop failed: %v", err)})
		return nil, fmt.Errorf("pipeline step 4 (critique) failed: %w", err)
	}
	slog.Info("Teacher critique completed", "run_id", runID, "sections_reviewed", len(refined), "duration", time.Since(t3))

	// Step 5: Report Assembly & Formatting (Phase 8)
	t4 := time.Now()
	reportMD, err := o.AssembleReport(ctx, runID)
	if err != nil {
		_ = o.store.UpdateRunError(runID, fmt.Sprintf("Report assembly failed: %v", err))
		o.emitEvent(runID, "error", map[string]string{"error_message": fmt.Sprintf("Report assembly failed: %v", err)})
		return nil, fmt.Errorf("pipeline step 5 (assembly) failed: %w", err)
	}
	slog.Info("Teacher report assembled successfully", "run_id", runID, "report_len", len(reportMD), "duration", time.Since(t4), "total_duration", time.Since(start))

	o.emitEvent(runID, "done", map[string]interface{}{
		"run_id":     runID,
		"status":     RunStatusDone,
		"report_len": len(reportMD),
	})

	// Return updated completed run
	return o.store.GetRun(runID)
}

// RegenerateSection re-drafts, critiques, and re-assembles the report for a single section.
func (o *Orchestrator) RegenerateSection(ctx context.Context, runID string, sectionID string) (*TeacherSection, *TeacherRun, error) {
	if o.store == nil {
		return nil, nil, errors.New("teacher store is not initialized")
	}

	outline, err := o.store.GetOutline(runID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch outline for run %s: %w", runID, err)
	}

	var targetOutline *TeacherOutlineSection
	for i := range outline {
		if outline[i].ID == sectionID {
			targetOutline = &outline[i]
			break
		}
	}
	if targetOutline == nil {
		return nil, nil, fmt.Errorf("section %s not found in run %s", sectionID, runID)
	}

	slog.Info("Regenerating section", "run_id", runID, "section_id", sectionID, "title", targetOutline.Title)

	// Step 1: Draft Section
	draftSec, err := o.DraftSection(ctx, runID, targetOutline)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to re-draft section: %w", err)
	}

	// Step 2: Critique & Refine Section
	finalSec, err := o.CritiqueAndRefineSection(ctx, runID, targetOutline.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to critique regenerated section: %w", err)
	}

	// Step 3: Re-assemble Report
	if _, err := o.AssembleReport(ctx, runID); err != nil {
		return nil, nil, fmt.Errorf("failed to re-assemble report after section regeneration: %w", err)
	}

	updatedRun, err := o.store.GetRun(runID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch updated run %s: %w", runID, err)
	}

	_ = draftSec
	return finalSec, updatedRun, nil
}

func cleanActionJSON(respStr string) string {
	clean := strings.TrimSpace(respStr)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```JSON")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	if json.Valid([]byte(clean)) {
		return clean
	}

	// Extract outermost JSON object if surrounded by extra text
	firstIdx := strings.Index(clean, "{")
	lastIdx := strings.LastIndex(clean, "}")
	if firstIdx != -1 && lastIdx != -1 && lastIdx > firstIdx {
		candidate := clean[firstIdx : lastIdx+1]
		if json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	return clean
}
