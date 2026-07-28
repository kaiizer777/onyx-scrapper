package research

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
	"github.com/kaiizer777/onyx-scrapper/internal/search"
	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

type Options struct {
	MaxSubQuestions     int
	MaxReflectionRounds int
	MaxTotalSteps       int
	ResumeRunID         int64
}

type Orchestrator struct {
	client      *llm.Client
	store       *store.Store
	searchSvc   *search.Service
	planner     *Planner
	worker      *Worker
	synthesizer *Synthesizer
}

func NewOrchestrator(client *llm.Client, st *store.Store, searchSvc *search.Service) *Orchestrator {
	return &Orchestrator{
		client:      client,
		store:       st,
		searchSvc:   searchSvc,
		planner:     NewPlanner(client),
		worker:      NewWorker(client, st, searchSvc),
		synthesizer: NewSynthesizer(client),
	}
}

func (o *Orchestrator) Run(ctx context.Context, goal string, opts Options) (*store.ResearchRun, error) {
	if opts.MaxSubQuestions == 0 {
		opts.MaxSubQuestions = 6
	}
	if opts.MaxReflectionRounds == 0 {
		opts.MaxReflectionRounds = 2
	}

	var runID int64
	var err error
	var plan ResearchPlan

	if opts.ResumeRunID > 0 {
		runID = opts.ResumeRunID
		slog.Info("Resuming research run", "run_id", runID)
		run, err := o.store.GetResearchRun(runID)
		if err != nil || run == nil {
			return nil, fmt.Errorf("failed to resume run %d: %v", runID, err)
		}
		goal = run.Goal
		plan.Goal = goal
		// Fallback outline if missing
		plan.ReportOutline = []string{"Introduction", "Findings", "Conclusion"}
		
		dbSqs, _ := o.store.GetSubQuestionsForRun(runID)
		for _, dbSq := range dbSqs {
			plan.SubQuestions = append(plan.SubQuestions, SubQuestion{
				ID:       dbSq.ID,
				Question: dbSq.Question,
				Status:   dbSq.Status,
			})
		}
	} else {
		runID, err = o.store.CreateResearchRun(goal)
		if err != nil {
			return nil, fmt.Errorf("failed to create research run: %w", err)
		}

		slog.Info("Planning research", "goal", goal)
		plan, err = o.planner.Plan(ctx, goal)
		if err != nil {
			_ = o.store.UpdateResearchRunStatus(runID, "failed", "")
			return nil, fmt.Errorf("planning failed: %w", err)
		}

		if len(plan.SubQuestions) > opts.MaxSubQuestions {
			plan.SubQuestions = plan.SubQuestions[:opts.MaxSubQuestions]
		}

		for i, sq := range plan.SubQuestions {
			sqID, _ := o.store.CreateSubQuestion(runID, sq.Question)
			plan.SubQuestions[i].ID = sqID
			plan.SubQuestions[i].Status = "pending"
		}
	}

	reflectionRounds := 0
	totalQuestionsGenerated := len(plan.SubQuestions)

	for {
		dbSqs, _ := o.store.GetSubQuestionsForRun(runID)
		var pendingSqs []store.ResearchSubQuestion
		for _, sq := range dbSqs {
			if sq.Status == "pending" || sq.Status == "failed" {
				pendingSqs = append(pendingSqs, sq)
			}
		}

		if len(pendingSqs) > 0 {
			slog.Info("Executing research branches", "count", len(pendingSqs))
			o.executeParallelResearch(ctx, runID, pendingSqs)
		}

		if reflectionRounds >= opts.MaxReflectionRounds {
			slog.Info("Max reflection rounds reached")
			break
		}

		allFindings, _ := o.store.GetAllFindingsForRun(runID)
		slog.Info("Reflecting on findings", "findings_count", len(allFindings))

		newQuestions, err := o.planner.ReflectAndReplan(ctx, plan, allFindings)
		if err != nil {
			slog.Warn("Reflection failed, proceeding to synthesis", "error", err)
			break
		}

		if len(newQuestions) == 0 {
			slog.Info("Reflection found sufficient coverage")
			break
		}

		slog.Info("Reflection generated new questions", "count", len(newQuestions))
		added := 0
		for _, nq := range newQuestions {
			if totalQuestionsGenerated >= opts.MaxSubQuestions {
				break
			}
			o.store.CreateSubQuestion(runID, nq)
			totalQuestionsGenerated++
			added++
		}

		if added == 0 {
			break
		}

		reflectionRounds++
	}

	allFindings, _ := o.store.GetAllFindingsForRun(runID)
	slog.Info("Synthesizing final report", "findings", len(allFindings))

	report, err := o.synthesizer.Synthesize(ctx, plan, allFindings)
	if err != nil {
		_ = o.store.UpdateResearchRunStatus(runID, "failed", "")
		return nil, fmt.Errorf("synthesis failed: %w", err)
	}

	_ = o.store.UpdateResearchRunStatus(runID, "completed", report)
	return o.store.GetResearchRun(runID)
}

func (o *Orchestrator) executeParallelResearch(ctx context.Context, runID int64, sqs []store.ResearchSubQuestion) {
	var wg sync.WaitGroup
	concurrency := 3
	sem := make(chan struct{}, concurrency)

	for _, sq := range sqs {
		wg.Add(1)
		go func(q store.ResearchSubQuestion) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			_ = o.store.UpdateSubQuestionStatus(q.ID, "running")
			slog.Info("Starting sub-research", "question", q.Question)
			
			err := o.worker.RunSubResearch(ctx, runID, q.ID, q.Question)
			
			if err != nil {
				slog.Warn("Sub-research failed", "question", q.Question, "error", err)
				_ = o.store.UpdateSubQuestionStatus(q.ID, "failed")
			} else {
				slog.Info("Completed sub-research", "question", q.Question)
				_ = o.store.UpdateSubQuestionStatus(q.ID, "done")
			}
		}(sq)
	}

	wg.Wait()
}
