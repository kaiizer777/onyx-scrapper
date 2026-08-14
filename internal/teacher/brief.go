package teacher

import (
	"errors"
	"fmt"
)

// ErrRunNotFound is returned when a teacher run cannot be found.
var ErrRunNotFound = errors.New("teacher run not found")

// ErrBriefNotReady is returned when a brief has not yet been compiled or is nil.
var ErrBriefNotReady = errors.New("learning brief is not ready or does not exist")

// GetBrief retrieves the compiled LearningBrief for a given run ID.
func (o *Orchestrator) GetBrief(runID string) (*LearningBrief, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}

	run, err := o.store.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch run %s: %w", runID, err)
	}
	if run == nil {
		return nil, ErrRunNotFound
	}
	if run.LearningBrief == nil {
		return nil, ErrBriefNotReady
	}

	return run.LearningBrief, nil
}

// PatchBrief applies a mutation function to the compiled LearningBrief of a run,
// validates the mutated brief, and persists it back to the database.
func (o *Orchestrator) PatchBrief(runID string, patchFn func(b *LearningBrief) error) (*LearningBrief, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}
	if patchFn == nil {
		return nil, errors.New("patch function cannot be nil")
	}

	currentBrief, err := o.GetBrief(runID)
	if err != nil {
		return nil, err
	}

	// Create a shallow copy of the brief to avoid modifying the in-memory object on validation failure.
	clonedBrief := *currentBrief

	if err := patchFn(&clonedBrief); err != nil {
		return nil, fmt.Errorf("patch function failed: %w", err)
	}

	if err := clonedBrief.Validate(); err != nil {
		return nil, fmt.Errorf("patched brief validation failed: %w", err)
	}

	if err := o.store.UpdateRunBrief(runID, &clonedBrief); err != nil {
		return nil, fmt.Errorf("failed to update brief in store: %w", err)
	}

	return &clonedBrief, nil
}

// PatchBriefDirect replaces the LearningBrief of a run with an updated brief,
// validates the schema, and persists it to SQLite.
func (o *Orchestrator) PatchBriefDirect(runID string, updated *LearningBrief) (*LearningBrief, error) {
	if updated == nil {
		return nil, errors.New("updated brief cannot be nil")
	}

	return o.PatchBrief(runID, func(b *LearningBrief) error {
		*b = *updated
		return nil
	})
}
