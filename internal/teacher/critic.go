package teacher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
)

// CritiqueAndRefineSection performs the evaluator-optimizer critique loop on a drafted section.
// It evaluates the draft against the 5-dimension rubric, prompts for targeted revisions if
// issues are found, and terminates when approved or upon reaching critique_pass_limit.
func (o *Orchestrator) CritiqueAndRefineSection(ctx context.Context, runID string, sectionID string) (*TeacherSection, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}
	if o.client == nil {
		return nil, errors.New("llm client is not initialized")
	}

	brief, err := o.GetBrief(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch brief for run %s: %w", runID, err)
	}

	// Resolve outline section
	outline, err := o.store.GetOutline(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch outline for run %s: %w", runID, err)
	}

	var targetOutline *TeacherOutlineSection
	for i := range outline {
		if outline[i].ID == sectionID {
			targetOutline = &outline[i]
			break
		}
	}
	if targetOutline == nil {
		return nil, fmt.Errorf("outline section %s not found in run %s", sectionID, runID)
	}

	// Resolve existing section draft
	sections, err := o.store.GetSectionsForRun(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch sections for run %s: %w", runID, err)
	}

	var sec *TeacherSection
	for i := range sections {
		if sections[i].OutlineID == targetOutline.ID || sections[i].ID == sectionID {
			sec = &sections[i]
			break
		}
	}
	if sec == nil || strings.TrimSpace(sec.DraftMD) == "" {
		return nil, fmt.Errorf("no draft found for section %s (%s)", targetOutline.ID, targetOutline.Title)
	}

	// Fetch section findings for factual grounding check
	findings, err := o.store.GetFindingsForSection(targetOutline.ID)
	if err != nil {
		slog.Warn("Failed to fetch section findings for critique", "section_id", targetOutline.ID, "error", err)
	}

	critiquePassLimit := 2
	if o.cfg != nil {
		tCfg := o.cfg.GetTeacherConfig()
		if tCfg.CritiquePassLimit > 0 {
			critiquePassLimit = tCfg.CritiquePassLimit
		}
	}

	currentDraft := sec.DraftMD
	var accumulatedNotes []CritiqueNote

	for {
		o.emitEvent(runID, "section_critiquing", map[string]string{
			"section_id": targetOutline.ID,
			"title":      targetOutline.Title,
		})

		// Step 1: Critic Evaluation LLM Call
		sysPrompt, userPrompt := BuildCriticPrompt(brief, targetOutline, currentDraft, findings)
		messages := []llm.Message{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		}

		respStr, err := o.client.Chat(ctx, messages)
		if err != nil {
			return nil, fmt.Errorf("critic LLM chat failed for section %s: %w", targetOutline.Title, err)
		}

		cleanJSON := cleanActionJSON(respStr)
		var critiqueResp CritiqueEvaluationResponse
		if err := json.Unmarshal([]byte(cleanJSON), &critiqueResp); err != nil {
			slog.Warn("Failed to parse critique response JSON; accepting draft with fallback pass", "error", err, "raw", cleanJSON)
			critiqueResp.Verdict = "pass"
		}

		accumulatedNotes = critiqueResp.Issues

		// Check if revision is required:
		// Verdict is revise OR any major issue present
		needsRevision := strings.ToLower(strings.TrimSpace(critiqueResp.Verdict)) == "revise"
		if !needsRevision {
			for _, item := range critiqueResp.Issues {
				if strings.ToLower(strings.TrimSpace(item.Severity)) == "major" {
					needsRevision = true
					break
				}
			}
		}

		// If approved, finalize and persist
		if !needsRevision {
			sec.FinalMD = currentDraft
			sec.CritiqueNotes = accumulatedNotes
			if err := o.store.UpdateSectionCritique(sec.ID, sec.CritiqueNotes, sec.FinalMD, sec.RevisionCount); err != nil {
				return nil, fmt.Errorf("failed to persist approved section critique: %w", err)
			}
			_ = o.store.UpdateOutlineSectionStatus(targetOutline.ID, OutlineStatusDone)
			o.emitEvent(runID, "section_done", map[string]interface{}{
				"section_id": targetOutline.ID,
				"title":      targetOutline.Title,
				"final_md":   sec.FinalMD,
			})
			return sec, nil
		}

		// Hard stop ceiling: terminate if revision limit reached
		if sec.RevisionCount >= critiquePassLimit {
			slog.Info("Critique pass limit reached, accepting current draft as final",
				"section_id", targetOutline.ID, "revision_count", sec.RevisionCount, "limit", critiquePassLimit)
			sec.FinalMD = currentDraft
			sec.CritiqueNotes = accumulatedNotes
			if err := o.store.UpdateSectionCritique(sec.ID, sec.CritiqueNotes, sec.FinalMD, sec.RevisionCount); err != nil {
				return nil, fmt.Errorf("failed to persist section critique at pass limit: %w", err)
			}
			_ = o.store.UpdateOutlineSectionStatus(targetOutline.ID, OutlineStatusDone)
			o.emitEvent(runID, "section_done", map[string]interface{}{
				"section_id": targetOutline.ID,
				"title":      targetOutline.Title,
				"final_md":   sec.FinalMD,
			})
			return sec, nil
		}

		// Step 2: Section Writer Targeted Revision
		sec.RevisionCount++
		o.emitEvent(runID, "section_revised", map[string]interface{}{
			"section_id":     targetOutline.ID,
			"title":          targetOutline.Title,
			"revision_count": sec.RevisionCount,
			"issues":         critiqueResp.Issues,
		})

		revSysPrompt, revUserPrompt := BuildSectionRevisionPrompt(brief, targetOutline, currentDraft, findings, critiqueResp.Issues)
		revMessages := []llm.Message{
			{Role: "system", Content: revSysPrompt},
			{Role: "user", Content: revUserPrompt},
		}

		revRespStr, err := o.client.Chat(ctx, revMessages)
		if err != nil {
			return nil, fmt.Errorf("section revision LLM chat failed for section %s: %w", targetOutline.Title, err)
		}

		currentDraft = cleanMarkdownContent(revRespStr)
		if currentDraft == "" {
			return nil, fmt.Errorf("revised draft for section %s was empty", targetOutline.Title)
		}

		// Persist updated draft
		sec.DraftMD = currentDraft
		_ = o.store.SaveSectionDraft(sec)
	}
}

// CritiqueAllSections executes the critique-refine loop across all outline sections of a run.
func (o *Orchestrator) CritiqueAllSections(ctx context.Context, runID string) ([]TeacherSection, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}

	outline, err := o.store.GetOutline(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch outline for run %s: %w", runID, err)
	}
	if len(outline) == 0 {
		return nil, fmt.Errorf("no outline sections found for run %s", runID)
	}

	// Update run status to critiquing
	if err := o.store.UpdateRunStatus(runID, RunStatusCritiquing); err != nil {
		return nil, fmt.Errorf("failed to update run status to critiquing (%s): %w", runID, err)
	}

	concurrency := 4
	if o.cfg != nil {
		tCfg := o.cfg.GetTeacherConfig()
		if tCfg.SectionWorkerConcurrency > 0 {
			concurrency = tCfg.SectionWorkerConcurrency
		}
	}

	var mu sync.Mutex
	results := make([]TeacherSection, len(outline))

	var eg errgroup.Group
	eg.SetLimit(concurrency)

	for idx, sec := range outline {
		i := idx
		section := sec
		eg.Go(func() error {
			refined, err := o.CritiqueAndRefineSection(ctx, runID, section.ID)
			if err != nil {
				return fmt.Errorf("critique failed for section %s (%s): %w", section.ID, section.Title, err)
			}
			mu.Lock()
			results[i] = *refined
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, fmt.Errorf("critique all sections encountered error: %w", err)
	}

	return results, nil
}
