package teacher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/kaiizer777/onyx-scrapper/internal/llm"
)

const (
	MinOutlineSections = 4
	MaxOutlineSections = 10
	MaxOutlineRetries  = 3
)

// GenerateOutline plans, topologically sorts, and persists teaching outline sections for a run.
func (o *Orchestrator) GenerateOutline(ctx context.Context, runID string) ([]TeacherOutlineSection, error) {
	if o.store == nil {
		return nil, errors.New("teacher store is not initialized")
	}
	if o.client == nil {
		return nil, errors.New("llm client is not initialized")
	}

	brief, err := o.GetBrief(runID)
	if err != nil {
		return nil, fmt.Errorf("cannot generate outline without learning brief: %w", err)
	}

	systemPrompt, userPrompt := BuildOutlinePlannerPrompt(brief)
	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	var rawSections []OutlinePlannerSection
	var lastErr error

	for attempt := 0; attempt < MaxOutlineRetries; attempt++ {
		respStr, err := o.client.Chat(ctx, messages)
		if err != nil {
			return nil, fmt.Errorf("outline planner llm chat failed: %w", err)
		}

		cleanJSON := cleanActionJSON(respStr)

		var plannerResp OutlinePlannerResponse
		if err := json.Unmarshal([]byte(cleanJSON), &plannerResp); err != nil {
			// Fallback: try parsing as direct array of sections
			var directSections []OutlinePlannerSection
			if errDirect := json.Unmarshal([]byte(cleanJSON), &directSections); errDirect == nil && len(directSections) > 0 {
				plannerResp.Sections = directSections
			} else {
				lastErr = fmt.Errorf("failed to parse outline response JSON: %w (raw: %s)", err, cleanJSON)
				messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
				messages = append(messages, llm.Message{Role: "user", Content: fmt.Sprintf("Invalid JSON: %v. Please output strictly valid JSON conforming to the schema.", err)})
				continue
			}
		}

		if len(plannerResp.Sections) == 0 {
			lastErr = errors.New("outline planner returned empty sections list")
			messages = append(messages, llm.Message{Role: "assistant", Content: respStr})
			messages = append(messages, llm.Message{Role: "user", Content: "The 'sections' array cannot be empty. Please provide 4 to 10 sections."})
			continue
		}

		rawSections = plannerResp.Sections
		break
	}

	if len(rawSections) == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("outline planner failed after %d retries: %w", MaxOutlineRetries, lastErr)
		}
		return nil, errors.New("outline planner failed to produce sections")
	}

	// Normalize and enforce Section 0
	normalizedSections := ensureSectionZero(rawSections, brief.Topic)

	// Topologically sort sections based on depends_on prerequisites
	sortedSections, err := TopologicallySortOutline(normalizedSections, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to sort outline sections: %w", err)
	}

	// Persist outline sections to SQLite
	if err := o.store.SaveOutline(sortedSections); err != nil {
		return nil, fmt.Errorf("failed to persist outline to store: %w", err)
	}

	// Update run status to researching
	if err := o.store.UpdateRunStatus(runID, RunStatusResearching); err != nil {
		return nil, fmt.Errorf("failed to update run status to researching: %w", err)
	}

	o.emitEvent(runID, "outline_ready", sortedSections)

	return sortedSections, nil
}

// ensureSectionZero checks for Section 0 inclusion and prepends it if missing or misconfigured.
func ensureSectionZero(sections []OutlinePlannerSection, topic string) []OutlinePlannerSection {
	var sec0Found bool
	var sec0Idx int

	for i, s := range sections {
		titleLower := strings.ToLower(s.Title)
		if strings.Contains(titleLower, "why this matters") || strings.Contains(titleLower, "core intuition") || s.ID == "sec_0" {
			sec0Found = true
			sec0Idx = i
			break
		}
	}

	if sec0Found {
		// Ensure Section 0 has canonical title, no prerequisites, and is first in the list
		sec0 := sections[sec0Idx]
		sec0.Title = SectionZeroTitle
		sec0.DependsOn = nil
		if sec0.ID == "" {
			sec0.ID = "sec_0"
		}
		if strings.TrimSpace(sec0.LearningObjective) == "" {
			sec0.LearningObjective = fmt.Sprintf("Grasp the core intuition, motivation, and mental model of %s before diving into technical details.", topic)
		}

		// Reorder so sec0 is at index 0
		var reordered []OutlinePlannerSection
		reordered = append(reordered, sec0)
		for i, s := range sections {
			if i != sec0Idx {
				reordered = append(reordered, s)
			}
		}
		return reordered
	}

	// Section 0 not found, prepend default Section 0
	defaultSec0 := OutlinePlannerSection{
		ID:                "sec_0",
		Title:             SectionZeroTitle,
		LearningObjective: fmt.Sprintf("Grasp the core intuition, motivation, and mental model of %s before diving into technical details.", topic),
		DependsOn:         nil,
	}

	return append([]OutlinePlannerSection{defaultSec0}, sections...)
}

// TopologicallySortOutline sorts outline sections in prerequisite-first order using Kahn's algorithm.
// It detects cycles and gracefully resolves them without failing.
func TopologicallySortOutline(sections []OutlinePlannerSection, runID string) ([]TeacherOutlineSection, error) {
	if len(sections) == 0 {
		return nil, errors.New("cannot sort empty outline")
	}

	sections = ensureSectionZero(sections, "")

	// Map section temporary IDs
	sectionMap := make(map[string]OutlinePlannerSection)
	idOrder := make([]string, 0, len(sections))
	idToIndex := make(map[string]int)

	for i, s := range sections {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			id = fmt.Sprintf("sec_%d", i)
			s.ID = id
		}
		sectionMap[id] = s
		idOrder = append(idOrder, id)
		idToIndex[id] = i
	}

	// Build adjacency and in-degree maps
	inDegree := make(map[string]int)
	adj := make(map[string][]string)       // u -> list of v that depend on u
	cleanDeps := make(map[string][]string) // v -> list of prerequisites u

	for _, id := range idOrder {
		inDegree[id] = 0
		adj[id] = nil
		cleanDeps[id] = nil
	}

	for _, s := range sections {
		id := s.ID
		for _, rawDep := range s.DependsOn {
			dep := strings.TrimSpace(rawDep)
			if dep == "" || dep == id {
				continue
			}
			if _, exists := sectionMap[dep]; exists {
				adj[dep] = append(adj[dep], id)
				inDegree[id]++
				cleanDeps[id] = append(cleanDeps[id], dep)
			}
		}
	}

	// Priority queue / slice of available nodes (in-degree == 0), sorted by original index
	var readyQueue []string
	for _, id := range idOrder {
		if inDegree[id] == 0 {
			readyQueue = append(readyQueue, id)
		}
	}

	sortQueue := func(q []string) {
		sort.SliceStable(q, func(i, j int) bool {
			return idToIndex[q[i]] < idToIndex[q[j]]
		})
	}
	sortQueue(readyQueue)

	var orderedIDs []string
	visited := make(map[string]bool)

	for len(readyQueue) > 0 {
		// Pop first
		curr := readyQueue[0]
		readyQueue = readyQueue[1:]

		if visited[curr] {
			continue
		}
		visited[curr] = true
		orderedIDs = append(orderedIDs, curr)

		for _, neighbor := range adj[curr] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 && !visited[neighbor] {
				readyQueue = append(readyQueue, neighbor)
			}
		}
		sortQueue(readyQueue)
	}

	// Handle cycle resolution: if not all nodes visited, append remaining nodes in original index order
	if len(orderedIDs) < len(idOrder) {
		slog.Warn("Topological sort detected dependency cycle in outline; breaking cycles gracefully", "total", len(idOrder), "resolved", len(orderedIDs))
		for _, id := range idOrder {
			if !visited[id] {
				orderedIDs = append(orderedIDs, id)
				visited[id] = true
				// Clear cyclical dependencies that could not be satisfied
				cleanDeps[id] = nil
			}
		}
	}

	// Ensure Section 0 is always at index 0
	var sec0ID string
	for _, id := range orderedIDs {
		sec := sectionMap[id]
		if sec.Title == SectionZeroTitle || id == "sec_0" {
			sec0ID = id
			break
		}
	}

	if sec0ID != "" && orderedIDs[0] != sec0ID {
		var reordered []string
		reordered = append(reordered, sec0ID)
		for _, id := range orderedIDs {
			if id != sec0ID {
				reordered = append(reordered, id)
			}
		}
		orderedIDs = reordered
	}

	// Generate persistent SQLite IDs and map prerequisites
	tempToDbID := make(map[string]string)
	for _, tempID := range orderedIDs {
		tempToDbID[tempID] = generateID("to")
	}

	var result []TeacherOutlineSection
	for order, tempID := range orderedIDs {
		rawSec := sectionMap[tempID]

		// Map prerequisite temp IDs to persistent DB IDs
		var mappedDeps []string
		for _, dep := range cleanDeps[tempID] {
			if dbID, ok := tempToDbID[dep]; ok {
				mappedDeps = append(mappedDeps, dbID)
			}
		}

		result = append(result, TeacherOutlineSection{
			ID:                tempToDbID[tempID],
			RunID:             runID,
			SectionOrder:      order,
			Title:             rawSec.Title,
			LearningObjective: rawSec.LearningObjective,
			DependsOn:         strings.Join(mappedDeps, ","),
			Status:            OutlineStatusPending,
		})
	}

	return result, nil
}
