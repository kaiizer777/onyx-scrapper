package teacher

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Run status constants matching §3 of work.md.
const (
	RunStatusClarifying  = "clarifying"
	RunStatusBriefReady  = "brief_ready"
	RunStatusResearching = "researching"
	RunStatusWriting     = "writing"
	RunStatusCritiquing  = "critiquing"
	RunStatusAssembling  = "assembling"
	RunStatusDone        = "done"
	RunStatusError       = "error"
)

// Outline section status constants.
const (
	OutlineStatusPending    = "pending"
	OutlineStatusDrafting   = "drafting"
	OutlineStatusCritiquing = "critiquing"
	OutlineStatusDone       = "done"
)

// Clarification input kinds.
const (
	InputKindSingleSelect = "single_select"
	InputKindMultiSelect  = "multi_select"
	InputKindFreeText     = "free_text"
)

// FormatPreferences defines the formatting targets for the generated report.
type FormatPreferences struct {
	Length            string `json:"length"`                       // "short" | "medium" | "long"
	WantsCodeExamples *bool  `json:"wants_code_examples,omitempty"` // null if not applicable to domain
	WantsDiagrams     bool   `json:"wants_diagrams"`
}

// LearningBrief is the contract between clarification phase and pipeline (§5).
type LearningBrief struct {
	Topic                string            `json:"topic"`
	Domain               string            `json:"domain"`
	LearnerLevel         string            `json:"learner_level"`
	Motivation           string            `json:"motivation"`
	Depth                string            `json:"depth"` // "overview" | "working_understanding" | "deep_dive"
	KnownReferencePoints []string          `json:"known_reference_points"`
	ExplicitScopeIn      []string          `json:"explicit_scope_in"`
	ExplicitScopeOut     []string          `json:"explicit_scope_out"`
	FormatPreferences    FormatPreferences `json:"format_preferences"`
	AssumptionsToAvoid   []string          `json:"assumptions_to_avoid"`
}

// Validate validates that the brief has the required fields and sane defaults.
func (b *LearningBrief) Validate() error {
	if b == nil {
		return errors.New("learning brief is nil")
	}
	if strings.TrimSpace(b.Topic) == "" {
		return errors.New("learning brief topic is required")
	}
	if strings.TrimSpace(b.Domain) == "" {
		return errors.New("learning brief domain is required")
	}
	if b.KnownReferencePoints == nil {
		b.KnownReferencePoints = []string{}
	}
	if b.ExplicitScopeIn == nil {
		b.ExplicitScopeIn = []string{}
	}
	if b.ExplicitScopeOut == nil {
		b.ExplicitScopeOut = []string{}
	}
	if b.AssumptionsToAvoid == nil {
		b.AssumptionsToAvoid = []string{}
	}
	if b.Depth == "" {
		b.Depth = "working_understanding"
	}
	if b.FormatPreferences.Length == "" {
		b.FormatPreferences.Length = "medium"
	}
	return nil
}

// ClarificationQuestion is a typed question emitted by the LLM ask_learner tool.
type ClarificationQuestion struct {
	Text      string   `json:"text"`
	InputKind string   `json:"input_kind"` // single_select | multi_select | free_text
	Options   []string `json:"options,omitempty"`
}

// ClarificationRound stores a single Q&A turn during clarification.
type ClarificationRound struct {
	ID        string                `json:"id"`
	RunID     string                `json:"run_id"`
	Round     int                   `json:"round"`
	Question  ClarificationQuestion `json:"question"`
	Answer    string                `json:"answer,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
}

// ClarificationResult is the result returned by a clarification turn.
type ClarificationResult struct {
	RunID    string                 `json:"run_id"`
	Status   string                 `json:"status"` // "clarifying" | "brief_ready" | "error"
	Round    int                    `json:"round"`
	Question *ClarificationQuestion `json:"question,omitempty"`
	Brief    *LearningBrief         `json:"brief,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

// TeacherRun represents a single Teacher Agent run in SQLite.
type TeacherRun struct {
	ID            string         `json:"id"`
	RawGoal       string         `json:"raw_goal"`
	Status        string         `json:"status"`
	LearningBrief *LearningBrief `json:"learning_brief,omitempty"`
	ReportMD      string         `json:"report_md,omitempty"`
	ErrorMessage  string         `json:"error_message,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	CompletedAt   *time.Time     `json:"completed_at,omitempty"`
}

// TeacherOutlineSection represents one section in the generated teaching outline.
type TeacherOutlineSection struct {
	ID                string `json:"id"`
	RunID             string `json:"run_id"`
	SectionOrder      int    `json:"section_order"`
	Title             string `json:"title"`
	LearningObjective string `json:"learning_objective"`
	DependsOn         string `json:"depends_on,omitempty"`
	Status            string `json:"status"` // pending | drafting | critiquing | done
}

// TeacherFinding represents an authoritative claim scoped to an outline section.
type TeacherFinding struct {
	ID             string    `json:"id"`
	RunID          string    `json:"run_id"`
	SectionID      string    `json:"section_id"`
	Claim          string    `json:"claim"`
	SourceURL      string    `json:"source_url,omitempty"`
	SourceProvider string    `json:"source_provider,omitempty"` // searxng | tinyfish | jina
	AuthorityTier  string    `json:"authority_tier,omitempty"`   // Primary | Established | General
	Confidence     float64   `json:"confidence"`
	CreatedAt      time.Time `json:"created_at"`
}

// CritiqueNote holds a single feedback item from the evaluator-optimizer loop.
type CritiqueNote struct {
	Issue      string `json:"issue"`
	Severity   string `json:"severity"` // "minor" | "major"
	Suggestion string `json:"suggestion"`
}

// TeacherSection stores drafts, critique notes, and the final approved content of an outline section.
type TeacherSection struct {
	ID            string         `json:"id"`
	RunID         string         `json:"run_id"`
	OutlineID     string         `json:"outline_id"`
	DraftMD       string         `json:"draft_md,omitempty"`
	CritiqueNotes []CritiqueNote `json:"critique_notes,omitempty"`
	FinalMD       string         `json:"final_md,omitempty"`
	RevisionCount int            `json:"revision_count"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// SearchResult represents a hit from the teacher FTS index.
type SearchResult struct {
	RunID        string `json:"run_id"`
	SectionTitle string `json:"section_title"`
	Snippet      string `json:"snippet"`
}

// Raw JSON unmarshaling helpers for DB storage.
func (c *ClarificationRound) QuestionJSON() string {
	b, _ := json.Marshal(c.Question)
	return string(b)
}

func (s *TeacherSection) CritiqueNotesJSON() string {
	b, _ := json.Marshal(s.CritiqueNotes)
	return string(b)
}
