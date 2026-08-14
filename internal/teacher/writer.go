package teacher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
)

// DraftSection drafts educational content for a single outline section.
func (o *Orchestrator) DraftSection(ctx context.Context, runID string, section *TeacherOutlineSection) (*TeacherSection, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}
	if o.client == nil {
		return nil, errors.New("llm client is not initialized")
	}
	if section == nil {
		return nil, errors.New("outline section cannot be nil")
	}

	brief, err := o.GetBrief(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to get brief for run %s: %w", runID, err)
	}

	findings, err := o.store.GetFindingsForSection(section.ID)
	if err != nil {
		slog.Warn("Failed to fetch section findings, proceeding with generic background", "section_id", section.ID, "error", err)
	}

	// Update outline section status to drafting
	_ = o.store.UpdateOutlineSectionStatus(section.ID, OutlineStatusDrafting)

	sysPrompt, userPrompt := BuildSectionWriterPrompt(brief, section, findings)
	messages := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPrompt},
	}

	respStr, err := o.client.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("llm drafting failed for section %q: %w", section.Title, err)
	}

	draftMD := cleanMarkdownContent(respStr)
	if draftMD == "" {
		return nil, fmt.Errorf("draft for section %q was empty", section.Title)
	}

	secID := generateID("ts")
	existingSections, _ := o.store.GetSectionsForRun(runID)
	for _, es := range existingSections {
		if es.OutlineID == section.ID || es.ID == section.ID {
			secID = es.ID
			break
		}
	}

	sec := &TeacherSection{
		ID:            secID,
		RunID:         runID,
		OutlineID:     section.ID,
		DraftMD:       draftMD,
		RevisionCount: 0,
	}

	if err := o.store.SaveSectionDraft(sec); err != nil {
		return nil, fmt.Errorf("failed to save draft for section %q: %w", section.Title, err)
	}

	// Advance outline section status to critiquing
	_ = o.store.UpdateOutlineSectionStatus(section.ID, OutlineStatusCritiquing)

	o.emitEvent(runID, "section_drafted", map[string]string{
		"section_id": section.ID,
		"title":      section.Title,
	})

	return sec, nil
}

// DraftAllSections coordinates parallel drafting across all outline sections of a run.
func (o *Orchestrator) DraftAllSections(ctx context.Context, runID string) ([]TeacherSection, error) {
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

	// Update run status to writing
	if err := o.store.UpdateRunStatus(runID, RunStatusWriting); err != nil {
		return nil, fmt.Errorf("failed to update run status to writing (%s): %w", runID, err)
	}

	concurrency := 4
	if o.cfg != nil {
		tCfg := o.cfg.GetTeacherConfig()
		if tCfg.SectionWorkerConcurrency > 0 {
			concurrency = tCfg.SectionWorkerConcurrency
		}
	}

	var mu sync.Mutex
	drafts := make([]TeacherSection, len(outline))

	var eg errgroup.Group
	eg.SetLimit(concurrency)

	for idx, sec := range outline {
		i := idx
		section := sec
		eg.Go(func() error {
			draft, err := o.DraftSection(ctx, runID, &section)
			if err != nil {
				return fmt.Errorf("drafting failed for section %s (%s): %w", section.ID, section.Title, err)
			}
			mu.Lock()
			drafts[i] = *draft
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return nil, fmt.Errorf("error drafting sections: %w", err)
	}

	return drafts, nil
}

func cleanMarkdownContent(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "```markdown") && strings.HasSuffix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```markdown")
		trimmed = strings.TrimSuffix(trimmed, "```")
		return strings.TrimSpace(trimmed)
	}
	if strings.HasPrefix(trimmed, "```md") && strings.HasSuffix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```md")
		trimmed = strings.TrimSuffix(trimmed, "```")
		return strings.TrimSpace(trimmed)
	}
	return trimmed
}
