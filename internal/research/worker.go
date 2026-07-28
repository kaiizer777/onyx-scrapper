package research

import (
	"context"
	"fmt"

	"github.com/kaiizer777/onyx-scrapper/internal/agent"
	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/search"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type Worker struct {
	client    *llm.Client
	store     *store.Store
	searchSvc *search.Service
}

func NewWorker(client *llm.Client, st *store.Store, searchSvc *search.Service) *Worker {
	return &Worker{
		client:    client,
		store:     st,
		searchSvc: searchSvc,
	}
}

func (w *Worker) RunSubResearch(ctx context.Context, runID int64, sqID int64, question string) error {
	// The agent will automatically persist findings because of WithSubQuestionID
	ag := agent.NewAgent(
		w.client, 
		w.store, 
		agent.WithMaxSteps(8), 
		agent.WithSearchService(w.searchSvc),
		agent.WithSubQuestionID(sqID),
	)
	
	// We pass 0 as existingRunID, which creates a dummy agent_runs record for execution steps.
	_, err := ag.Run(ctx, question, 0, nil)
	if err != nil {
		return fmt.Errorf("agent sub-research failed: %w", err)
	}

	return nil
}
