package teacher

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kaiizer777/onyx-scrapper/internal/store"
)

// Store handles SQLite persistence for the Teacher Agent.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

// NewStore creates a new Store instance with the provided database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// NewStoreFromAppStore initializes a teacher Store from the application's root store.
func NewStoreFromAppStore(st *store.Store) *Store {
	if st == nil {
		return nil
	}
	return &Store{db: st.DB()}
}

func generateID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

// CreateRun initializes a new teacher run in 'clarifying' status.
func (s *Store) CreateRun(rawGoal string) (*TeacherRun, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	id := generateID("tr")
	now := time.Now().UTC()

	query := `
		INSERT INTO teacher_runs (id, raw_goal, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?);
	`
	if _, err := s.db.Exec(query, id, rawGoal, RunStatusClarifying, now, now); err != nil {
		return nil, fmt.Errorf("failed to create teacher run: %w", err)
	}

	return &TeacherRun{
		ID:        id,
		RawGoal:   rawGoal,
		Status:    RunStatusClarifying,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// GetRun retrieves a teacher run by its ID.
func (s *Store) GetRun(id string) (*TeacherRun, error) {
	query := `
		SELECT id, raw_goal, status, learning_brief, report_md, error_message, created_at, updated_at, completed_at
		FROM teacher_runs
		WHERE id = ?;
	`
	row := s.db.QueryRow(query, id)

	var run TeacherRun
	var briefJSON sql.NullString
	var reportMD sql.NullString
	var errMsg sql.NullString
	var completedAt sql.NullTime

	err := row.Scan(
		&run.ID,
		&run.RawGoal,
		&run.Status,
		&briefJSON,
		&reportMD,
		&errMsg,
		&run.CreatedAt,
		&run.UpdatedAt,
		&completedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get teacher run %s: %w", id, err)
	}

	if briefJSON.Valid && briefJSON.String != "" {
		var brief LearningBrief
		if err := json.Unmarshal([]byte(briefJSON.String), &brief); err == nil {
			run.LearningBrief = &brief
		}
	}
	if reportMD.Valid {
		run.ReportMD = reportMD.String
	}
	if errMsg.Valid {
		run.ErrorMessage = errMsg.String
	}
	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}

	return &run, nil
}

// UpdateRunStatus updates the lifecycle status of a teacher run.
func (s *Store) UpdateRunStatus(id, status string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	var query string
	var args []interface{}

	if status == RunStatusDone || status == RunStatusError {
		query = `UPDATE teacher_runs SET status = ?, updated_at = ?, completed_at = ? WHERE id = ?;`
		args = []interface{}{status, now, now, id}
	} else {
		query = `UPDATE teacher_runs SET status = ?, updated_at = ? WHERE id = ?;`
		args = []interface{}{status, now, id}
	}

	res, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("failed to update teacher run status (%s): %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("teacher run %s not found", id)
	}
	return nil
}

// UpdateRunBrief stores the compiled learning brief and advances status to 'brief_ready'.
func (s *Store) UpdateRunBrief(id string, brief *LearningBrief) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if brief == nil {
		return errors.New("brief cannot be nil")
	}
	briefBytes, err := json.Marshal(brief)
	if err != nil {
		return fmt.Errorf("failed to marshal learning brief: %w", err)
	}

	now := time.Now().UTC()
	query := `
		UPDATE teacher_runs
		SET learning_brief = ?, status = ?, updated_at = ?
		WHERE id = ?;
	`
	res, err := s.db.Exec(query, string(briefBytes), RunStatusBriefReady, now, id)
	if err != nil {
		return fmt.Errorf("failed to update brief for run %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("teacher run %s not found", id)
	}
	return nil
}

// UpdateRunReport sets the final markdown report and marks run as 'done'.
func (s *Store) UpdateRunReport(id, reportMD string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	query := `
		UPDATE teacher_runs
		SET report_md = ?, status = ?, updated_at = ?, completed_at = ?
		WHERE id = ?;
	`
	res, err := s.db.Exec(query, reportMD, RunStatusDone, now, now, id)
	if err != nil {
		return fmt.Errorf("failed to update report for run %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("teacher run %s not found", id)
	}
	return nil
}

// UpdateRunError marks the run as failed with an error message.
func (s *Store) UpdateRunError(id, errMsg string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	query := `
		UPDATE teacher_runs
		SET error_message = ?, status = ?, updated_at = ?, completed_at = ?
		WHERE id = ?;
	`
	res, err := s.db.Exec(query, errMsg, RunStatusError, now, now, id)
	if err != nil {
		return fmt.Errorf("failed to record error for run %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("teacher run %s not found", id)
	}
	return nil
}

// ListRuns retrieves recent teacher runs ordered by newest first.
func (s *Store) ListRuns(limit int) ([]TeacherRun, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
		SELECT id, raw_goal, status, learning_brief, report_md, error_message, created_at, updated_at, completed_at
		FROM teacher_runs
		ORDER BY created_at DESC
		LIMIT ?;
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list teacher runs: %w", err)
	}
	defer rows.Close()

	var runs []TeacherRun
	for rows.Next() {
		var run TeacherRun
		var briefJSON sql.NullString
		var reportMD sql.NullString
		var errMsg sql.NullString
		var completedAt sql.NullTime

		if err := rows.Scan(
			&run.ID,
			&run.RawGoal,
			&run.Status,
			&briefJSON,
			&reportMD,
			&errMsg,
			&run.CreatedAt,
			&run.UpdatedAt,
			&completedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan teacher run: %w", err)
		}

		if briefJSON.Valid && briefJSON.String != "" {
			var brief LearningBrief
			if err := json.Unmarshal([]byte(briefJSON.String), &brief); err == nil {
				run.LearningBrief = &brief
			}
		}
		if reportMD.Valid {
			run.ReportMD = reportMD.String
		}
		if errMsg.Valid {
			run.ErrorMessage = errMsg.String
		}
		if completedAt.Valid {
			run.CompletedAt = &completedAt.Time
		}

		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating teacher runs: %w", err)
	}

	return runs, nil
}

// SaveClarification records a new clarification round question.
func (s *Store) SaveClarification(c *ClarificationRound) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if c.ID == "" {
		c.ID = generateID("tc")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO teacher_clarifications (id, run_id, round, question, answer, created_at)
		VALUES (?, ?, ?, ?, ?, ?);
	`
	_, err := s.db.Exec(query, c.ID, c.RunID, c.Round, c.QuestionJSON(), c.Answer, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to save clarification round: %w", err)
	}
	return nil
}

// UpdateClarificationAnswer updates learner answer for a given clarification round.
func (s *Store) UpdateClarificationAnswer(id, answer string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `UPDATE teacher_clarifications SET answer = ? WHERE id = ?;`
	res, err := s.db.Exec(query, answer, id)
	if err != nil {
		return fmt.Errorf("failed to update clarification answer: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("clarification %s not found", id)
	}
	return nil
}

// GetClarifications retrieves all clarification rounds for a run ordered by round ASC.
func (s *Store) GetClarifications(runID string) ([]ClarificationRound, error) {
	query := `
		SELECT id, run_id, round, question, answer, created_at
		FROM teacher_clarifications
		WHERE run_id = ?
		ORDER BY round ASC;
	`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query clarifications for run %s: %w", runID, err)
	}
	defer rows.Close()

	var rounds []ClarificationRound
	for rows.Next() {
		var round ClarificationRound
		var qJSON string
		var answer sql.NullString

		if err := rows.Scan(&round.ID, &round.RunID, &round.Round, &qJSON, &answer, &round.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan clarification round: %w", err)
		}
		_ = json.Unmarshal([]byte(qJSON), &round.Question)
		if answer.Valid {
			round.Answer = answer.String
		}
		rounds = append(rounds, round)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating clarifications: %w", err)
	}

	return rounds, nil
}

// SaveOutline persists the generated outline sections in a single transaction.
func (s *Store) SaveOutline(sections []TeacherOutlineSection) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin outline tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO teacher_outline (id, run_id, section_order, title, learning_objective, depends_on, status)
		VALUES (?, ?, ?, ?, ?, ?, ?);
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare outline insert: %w", err)
	}
	defer stmt.Close()

	for _, sec := range sections {
		id := sec.ID
		if id == "" {
			id = generateID("to")
		}
		status := sec.Status
		if status == "" {
			status = OutlineStatusPending
		}
		if _, err := stmt.Exec(id, sec.RunID, sec.SectionOrder, sec.Title, sec.LearningObjective, sec.DependsOn, status); err != nil {
			return fmt.Errorf("failed to insert outline section %s: %w", sec.Title, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit outline tx: %w", err)
	}
	return nil
}

// GetOutline retrieves all outline sections for a run ordered by section_order ASC.
func (s *Store) GetOutline(runID string) ([]TeacherOutlineSection, error) {
	query := `
		SELECT id, run_id, section_order, title, learning_objective, depends_on, status
		FROM teacher_outline
		WHERE run_id = ?
		ORDER BY section_order ASC;
	`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query outline for run %s: %w", runID, err)
	}
	defer rows.Close()

	var sections []TeacherOutlineSection
	for rows.Next() {
		var sec TeacherOutlineSection
		var dependsOn sql.NullString
		if err := rows.Scan(&sec.ID, &sec.RunID, &sec.SectionOrder, &sec.Title, &sec.LearningObjective, &dependsOn, &sec.Status); err != nil {
			return nil, fmt.Errorf("failed to scan outline section: %w", err)
		}
		if dependsOn.Valid {
			sec.DependsOn = dependsOn.String
		}
		sections = append(sections, sec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating outline sections: %w", err)
	}

	return sections, nil
}

// UpdateOutlineSectionStatus updates the status of an outline section.
func (s *Store) UpdateOutlineSectionStatus(id, status string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `UPDATE teacher_outline SET status = ? WHERE id = ?;`
	res, err := s.db.Exec(query, status, id)
	if err != nil {
		return fmt.Errorf("failed to update outline section status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("outline section %s not found", id)
	}
	return nil
}

// SaveFinding records a research claim linked to an outline section.
func (s *Store) SaveFinding(f *TeacherFinding) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if f.ID == "" {
		f.ID = generateID("tf")
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}

	query := `
		INSERT INTO teacher_findings (id, run_id, section_id, claim, source_url, source_provider, authority_tier, confidence, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := s.db.Exec(query, f.ID, f.RunID, f.SectionID, f.Claim, f.SourceURL, f.SourceProvider, f.AuthorityTier, f.Confidence, f.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to save teacher finding: %w", err)
	}
	return nil
}

// GetFindingsForSection retrieves all findings scoped to a single outline section.
func (s *Store) GetFindingsForSection(sectionID string) ([]TeacherFinding, error) {
	query := `
		SELECT id, run_id, section_id, claim, source_url, source_provider, authority_tier, confidence, created_at
		FROM teacher_findings
		WHERE section_id = ?
		ORDER BY created_at ASC;
	`
	rows, err := s.db.Query(query, sectionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query findings for section %s: %w", sectionID, err)
	}
	defer rows.Close()

	var findings []TeacherFinding
	for rows.Next() {
		var f TeacherFinding
		var srcURL, srcProv, authTier sql.NullString
		if err := rows.Scan(&f.ID, &f.RunID, &f.SectionID, &f.Claim, &srcURL, &srcProv, &authTier, &f.Confidence, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan finding: %w", err)
		}
		if srcURL.Valid {
			f.SourceURL = srcURL.String
		}
		if srcProv.Valid {
			f.SourceProvider = srcProv.String
		}
		if authTier.Valid {
			f.AuthorityTier = authTier.String
		}
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating findings: %w", err)
	}

	return findings, nil
}

// GetAllFindingsForRun retrieves all findings recorded across all sections of a run.
func (s *Store) GetAllFindingsForRun(runID string) ([]TeacherFinding, error) {
	query := `
		SELECT id, run_id, section_id, claim, source_url, source_provider, authority_tier, confidence, created_at
		FROM teacher_findings
		WHERE run_id = ?
		ORDER BY created_at ASC;
	`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query findings for run %s: %w", runID, err)
	}
	defer rows.Close()

	var findings []TeacherFinding
	for rows.Next() {
		var f TeacherFinding
		var srcURL, srcProv, authTier sql.NullString
		if err := rows.Scan(&f.ID, &f.RunID, &f.SectionID, &f.Claim, &srcURL, &srcProv, &authTier, &f.Confidence, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan finding: %w", err)
		}
		if srcURL.Valid {
			f.SourceURL = srcURL.String
		}
		if srcProv.Valid {
			f.SourceProvider = srcProv.String
		}
		if authTier.Valid {
			f.AuthorityTier = authTier.String
		}
		findings = append(findings, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating findings: %w", err)
	}

	return findings, nil
}

// SaveSectionDraft creates or updates an initial draft for an outline section.
func (s *Store) SaveSectionDraft(sec *TeacherSection) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if sec.ID == "" {
		sec.ID = generateID("ts")
	}
	now := time.Now().UTC()
	sec.CreatedAt = now
	sec.UpdatedAt = now

	query := `
		INSERT INTO teacher_sections (id, run_id, outline_id, draft_md, critique_notes, final_md, revision_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			draft_md = excluded.draft_md,
			updated_at = excluded.updated_at;
	`
	_, err := s.db.Exec(query, sec.ID, sec.RunID, sec.OutlineID, sec.DraftMD, sec.CritiqueNotesJSON(), sec.FinalMD, sec.RevisionCount, now, now)
	if err != nil {
		return fmt.Errorf("failed to save section draft: %w", err)
	}
	return nil
}

// UpdateSectionCritique saves critique notes, final approved markdown, and increments revision count.
func (s *Store) UpdateSectionCritique(id string, notes []CritiqueNote, finalMD string, revisionCount int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	notesBytes, _ := json.Marshal(notes)
	now := time.Now().UTC()

	query := `
		UPDATE teacher_sections
		SET critique_notes = ?, final_md = ?, revision_count = ?, updated_at = ?
		WHERE id = ?;
	`
	res, err := s.db.Exec(query, string(notesBytes), finalMD, revisionCount, now, id)
	if err != nil {
		return fmt.Errorf("failed to update section critique: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("teacher section %s not found", id)
	}
	return nil
}

// GetSection retrieves a section by its ID.
func (s *Store) GetSection(id string) (*TeacherSection, error) {
	query := `
		SELECT id, run_id, outline_id, draft_md, critique_notes, final_md, revision_count, created_at, updated_at
		FROM teacher_sections
		WHERE id = ?;
	`
	row := s.db.QueryRow(query, id)

	var sec TeacherSection
	var draftMD, critiqueJSON, finalMD sql.NullString
	if err := row.Scan(&sec.ID, &sec.RunID, &sec.OutlineID, &draftMD, &critiqueJSON, &finalMD, &sec.RevisionCount, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get section %s: %w", id, err)
	}

	if draftMD.Valid {
		sec.DraftMD = draftMD.String
	}
	if critiqueJSON.Valid && critiqueJSON.String != "" {
		_ = json.Unmarshal([]byte(critiqueJSON.String), &sec.CritiqueNotes)
	}
	if finalMD.Valid {
		sec.FinalMD = finalMD.String
	}

	return &sec, nil
}

// GetSectionsForRun retrieves all sections for a run.
func (s *Store) GetSectionsForRun(runID string) ([]TeacherSection, error) {
	query := `
		SELECT id, run_id, outline_id, draft_md, critique_notes, final_md, revision_count, created_at, updated_at
		FROM teacher_sections
		WHERE run_id = ?
		ORDER BY created_at ASC;
	`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query sections for run %s: %w", runID, err)
	}
	defer rows.Close()

	var sections []TeacherSection
	for rows.Next() {
		var sec TeacherSection
		var draftMD, critiqueJSON, finalMD sql.NullString
		if err := rows.Scan(&sec.ID, &sec.RunID, &sec.OutlineID, &draftMD, &critiqueJSON, &finalMD, &sec.RevisionCount, &sec.CreatedAt, &sec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan section: %w", err)
		}
		if draftMD.Valid {
			sec.DraftMD = draftMD.String
		}
		if critiqueJSON.Valid && critiqueJSON.String != "" {
			_ = json.Unmarshal([]byte(critiqueJSON.String), &sec.CritiqueNotes)
		}
		if finalMD.Valid {
			sec.FinalMD = finalMD.String
		}
		sections = append(sections, sec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sections: %w", err)
	}

	return sections, nil
}

// IndexReportFTS indexes finished section content for full-text search.
func (s *Store) IndexReportFTS(runID, sectionTitle, content string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	query := `INSERT INTO teacher_fts (run_id, section_title, content) VALUES (?, ?, ?);`
	if _, err := s.db.Exec(query, runID, sectionTitle, content); err != nil {
		return fmt.Errorf("failed to index teacher FTS: %w", err)
	}
	return nil
}

func sanitizeFTSQuery(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return ""
	}
	var sanitized []string
	for _, w := range words {
		clean := strings.ReplaceAll(w, `"`, "")
		if clean != "" {
			sanitized = append(sanitized, `"`+clean+`"`)
		}
	}
	return strings.Join(sanitized, " ")
}

// SearchFTS queries the teacher FTS5 virtual table.
func (s *Store) SearchFTS(query string, limit int) ([]SearchResult, error) {
	sanitized := sanitizeFTSQuery(query)
	if sanitized == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	q := `
		SELECT run_id, section_title, snippet(teacher_fts, 2, '<b>', '</b>', '...', 15) as snippet
		FROM teacher_fts
		WHERE teacher_fts MATCH ?
		ORDER BY rank
		LIMIT ?;
	`
	rows, err := s.db.Query(q, sanitized, limit)
	if err != nil {
		return nil, fmt.Errorf("teacher FTS search error for query %q: %w", query, err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var res SearchResult
		if err := rows.Scan(&res.RunID, &res.SectionTitle, &res.Snippet); err != nil {
			return nil, fmt.Errorf("error scanning teacher search row: %w", err)
		}
		results = append(results, res)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating teacher search rows: %w", err)
	}

	return results, nil
}

