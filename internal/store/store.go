package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type Page struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	FetchedAt time.Time `json:"fetched_at"`
	RawHTML        string    `json:"raw_html"`
	CleanText      string    `json:"clean_text"`
	SourceProvider string    `json:"source_provider"`
}

type Extraction struct {
	ID         int64     `json:"id"`
	PageID     int64     `json:"page_id"`
	SchemaName string    `json:"schema_name"`
	DataJSON   string    `json:"data_json"`
	CreatedAt  time.Time `json:"created_at"`
}

type SearchResult struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	Snippet   string    `json:"snippet"`
	FetchedAt time.Time `json:"fetched_at"`
}

type AgentRun struct {
	ID        int64     `json:"id"`
	Goal      string    `json:"goal"`
	Status    string    `json:"status"`
	Result    string    `json:"result"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentStep struct {
	ID         int64     `json:"id"`
	RunID      int64     `json:"run_id"`
	StepNumber int       `json:"step_number"`
	Action     string    `json:"action"`
	ArgsJSON   string    `json:"args_json"`
	Result     string    `json:"result"`
	Error      string    `json:"error"`
	CreatedAt  time.Time `json:"created_at"`
}

type ResearchRun struct {
	ID          int64      `json:"id"`
	Goal        string     `json:"goal"`
	Status      string     `json:"status"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ReportMD    string     `json:"report_md"`
}

type Stats struct {
	PagesScraped     int
	ExtractionsDone  int
	AgentRuns        int
	DeepResearchRuns int
}

type RunHistoryItem struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"` // "agent" or "research"
	Goal      string    `json:"goal"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
}

type ResearchSubQuestion struct {
	ID       int64  `json:"id"`
	RunID    int64  `json:"run_id"`
	Question string `json:"question"`
	Status   string `json:"status"` // pending, running, done, failed
}

type Finding struct {
	ID            int64     `json:"id"`
	SubQuestionID  int64     `json:"subquestion_id"`
	Claim          string    `json:"claim"`
	SourceURL      string    `json:"source_url"`
	SourceProvider string    `json:"source_provider"`
	Confidence     float64   `json:"confidence"`
	CreatedAt      time.Time `json:"created_at"`
}

type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

// NewStore initializes a SQLite database connection, ensuring directories exist and schema is applied.
func NewStore(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create db directory %s: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db at %s: %w", dbPath, err)
	}

	// Configure SQLite connection pragmas (WAL mode, busy timeout, foreign keys)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set pragmas: %w", err)
	}

	// Apply schema
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to execute schema: %w", err)
	}

	// Auto-migrate schema fixes
	db.Exec("ALTER TABLE findings ADD COLUMN source_provider TEXT;")

	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// SavePage inserts or updates a page record by URL and returns its database ID.
func (s *Store) SavePage(url, rawHTML, cleanText, sourceProvider string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()

	query := `
		INSERT INTO pages (url, fetched_at, raw_html, clean_text, source_provider)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET
			fetched_at = excluded.fetched_at,
			raw_html = excluded.raw_html,
			clean_text = excluded.clean_text,
			source_provider = excluded.source_provider
		RETURNING id;
	`

	var pageID int64
	err := s.db.QueryRow(query, url, now, rawHTML, cleanText, sourceProvider).Scan(&pageID)
	if err != nil {
		return 0, fmt.Errorf("failed to save page (%s): %w", url, err)
	}

	return pageID, nil
}

// GetPageByURL retrieves a saved page by its exact URL.
func (s *Store) GetPageByURL(url string) (*Page, error) {
	query := `SELECT id, url, fetched_at, raw_html, clean_text, source_provider FROM pages WHERE url = ?`
	row := s.db.QueryRow(query, url)

	var p Page
	err := row.Scan(&p.ID, &p.URL, &p.FetchedAt, &p.RawHTML, &p.CleanText, &p.SourceProvider)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get page by url %s: %w", url, err)
	}

	return &p, nil
}

// GetPageByID retrieves a saved page by its database ID.
func (s *Store) GetPageByID(id int64) (*Page, error) {
	query := `SELECT id, url, fetched_at, raw_html, clean_text, source_provider FROM pages WHERE id = ?`
	row := s.db.QueryRow(query, id)

	var p Page
	err := row.Scan(&p.ID, &p.URL, &p.FetchedAt, &p.RawHTML, &p.CleanText, &p.SourceProvider)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get page by id %d: %w", id, err)
	}

	return &p, nil
}

// GetRecentPages retrieves recent scraped pages.
func (s *Store) GetRecentPages(limit, offset int) ([]Page, error) {
	query := `
		SELECT id, url, fetched_at, raw_html, clean_text, source_provider 
		FROM pages 
		ORDER BY fetched_at DESC 
		LIMIT ? OFFSET ?
	`
	rows, err := s.db.Query(query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent pages: %w", err)
	}
	defer rows.Close()

	var pages []Page
	for rows.Next() {
		var p Page
		if err := rows.Scan(&p.ID, &p.URL, &p.FetchedAt, &p.RawHTML, &p.CleanText, &p.SourceProvider); err != nil {
			return nil, fmt.Errorf("failed to scan page: %w", err)
		}
		pages = append(pages, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating pages: %w", err)
	}

	return pages, nil
}

// SaveExtraction inserts a structured JSON extraction result linked to a page ID.
func (s *Store) SaveExtraction(pageID int64, schemaName, dataJSON string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	query := `
		INSERT INTO extractions (page_id, schema_name, data_json, created_at)
		VALUES (?, ?, ?, ?)
		RETURNING id;
	`

	var extractionID int64
	err := s.db.QueryRow(query, pageID, schemaName, dataJSON, now).Scan(&extractionID)
	if err != nil {
		return 0, fmt.Errorf("failed to save extraction for page_id %d: %w", pageID, err)
	}

	return extractionID, nil
}

// GetExtractionsForPage retrieves all extractions linked to a given page ID.
func (s *Store) GetExtractionsForPage(pageID int64) ([]Extraction, error) {
	query := `
		SELECT id, page_id, schema_name, data_json, created_at
		FROM extractions
		WHERE page_id = ?
		ORDER BY created_at DESC;
	`
	rows, err := s.db.Query(query, pageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query extractions for page %d: %w", pageID, err)
	}
	defer rows.Close()

	var extractions []Extraction
	for rows.Next() {
		var e Extraction
		if err := rows.Scan(&e.ID, &e.PageID, &e.SchemaName, &e.DataJSON, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan extraction: %w", err)
		}
		extractions = append(extractions, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating extractions: %w", err)
	}

	return extractions, nil
}

// SearchPages executes a SQLite FTS5 query over saved pages and returns matching results with snippets.
func (s *Store) SearchPages(userQuery string) ([]SearchResult, error) {
	if userQuery == "" {
		return nil, nil
	}

	query := `
		SELECT p.id, p.url, snippet(pages_fts, 1, '<b>', '</b>', '...', 15) as snippet, p.fetched_at
		FROM pages_fts f
		JOIN pages p ON p.id = f.rowid
		WHERE pages_fts MATCH ?
		ORDER BY rank
		LIMIT 20;
	`

	rows, err := s.db.Query(query, userQuery)
	if err != nil {
		return nil, fmt.Errorf("search error for query %q: %w", userQuery, err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var res SearchResult
		if err := rows.Scan(&res.ID, &res.URL, &res.Snippet, &res.FetchedAt); err != nil {
			return nil, fmt.Errorf("error scanning search row: %w", err)
		}
		results = append(results, res)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating search rows: %w", err)
	}

	return results, nil
}

// CreateAgentRun creates a new agent_runs record with status 'running' and returns its ID.
func (s *Store) CreateAgentRun(goal string) (int64, error) {
	now := time.Now().UTC()
	query := `
		INSERT INTO agent_runs (goal, status, result, created_at, updated_at)
		VALUES (?, 'running', '', ?, ?)
		RETURNING id;
	`
	var runID int64
	err := s.db.QueryRow(query, goal, now, now).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("failed to create agent run: %w", err)
	}
	return runID, nil
}

// UpdateAgentRunStatus updates status, result, and updated_at timestamp for an agent run.
func (s *Store) UpdateAgentRunStatus(runID int64, status, result string) error {
	now := time.Now().UTC()
	query := `
		UPDATE agent_runs
		SET status = ?, result = ?, updated_at = ?
		WHERE id = ?;
	`
	_, err := s.db.Exec(query, status, result, now, runID)
	if err != nil {
		return fmt.Errorf("failed to update agent run status (id %d): %w", runID, err)
	}
	return nil
}

// SaveAgentStep records an execution step of an agent run.
func (s *Store) SaveAgentStep(runID int64, stepNum int, action, argsJSON, result, stepErr string) (int64, error) {
	now := time.Now().UTC()
	query := `
		INSERT INTO agent_steps (run_id, step_number, action, args_json, result, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id;
	`
	var stepID int64
	err := s.db.QueryRow(query, runID, stepNum, action, argsJSON, result, stepErr, now).Scan(&stepID)
	if err != nil {
		return 0, fmt.Errorf("failed to save agent step (run_id %d, step %d): %w", runID, stepNum, err)
	}
	return stepID, nil
}

// GetAgentRun retrieves an agent run by ID.
func (s *Store) GetAgentRun(runID int64) (*AgentRun, error) {
	query := `SELECT id, goal, status, result, created_at, updated_at FROM agent_runs WHERE id = ?`
	row := s.db.QueryRow(query, runID)

	var run AgentRun
	err := row.Scan(&run.ID, &run.Goal, &run.Status, &run.Result, &run.CreatedAt, &run.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get agent run %d: %w", runID, err)
	}
	return &run, nil
}

// GetAgentSteps retrieves all steps recorded for a specific agent run ordered by step_number.
func (s *Store) GetAgentSteps(runID int64) ([]AgentStep, error) {
	query := `
		SELECT id, run_id, step_number, action, args_json, result, error, created_at
		FROM agent_steps
		WHERE run_id = ?
		ORDER BY step_number ASC;
	`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query agent steps for run %d: %w", runID, err)
	}
	defer rows.Close()

	var steps []AgentStep
	for rows.Next() {
		var st AgentStep
		if err := rows.Scan(&st.ID, &st.RunID, &st.StepNumber, &st.Action, &st.ArgsJSON, &st.Result, &st.Error, &st.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan agent step: %w", err)
		}
		steps = append(steps, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agent steps: %w", err)
	}

	return steps, nil
}

// GetAgentRuns retrieves all agent runs ordered by most recent first.
// Does not include result or steps — use GetAgentRun and GetAgentSteps for detail.
func (s *Store) GetAgentRuns(limit int) ([]AgentRun, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, goal, status, result, created_at, updated_at
		FROM agent_runs
		ORDER BY id DESC
		LIMIT ?;
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query agent runs: %w", err)
	}
	defer rows.Close()

	var runs []AgentRun
	for rows.Next() {
		var r AgentRun
		if err := rows.Scan(&r.ID, &r.Goal, &r.Status, &r.Result, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan agent run: %w", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agent runs: %w", err)
	}
	return runs, nil
}

// CreateResearchRun starts a new research run.
func (s *Store) CreateResearchRun(goal string) (int64, error) {
	now := time.Now().UTC()
	query := `INSERT INTO research_runs (goal, status, started_at, report_md) VALUES (?, 'running', ?, '') RETURNING id;`
	var runID int64
	err := s.db.QueryRow(query, goal, now).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("failed to create research run: %w", err)
	}
	return runID, nil
}

func (s *Store) UpdateResearchRunStatus(runID int64, status string, reportMD string) error {
	now := time.Now().UTC()
	var query string
	if status == "completed" || status == "failed" || status == "max_steps_exceeded" {
		query = `UPDATE research_runs SET status = ?, report_md = ?, completed_at = ? WHERE id = ?;`
		_, err := s.db.Exec(query, status, reportMD, now, runID)
		return err
	}
	query = `UPDATE research_runs SET status = ?, report_md = ? WHERE id = ?;`
	_, err := s.db.Exec(query, status, reportMD, runID)
	return err
}

func (s *Store) GetResearchRun(runID int64) (*ResearchRun, error) {
	query := `SELECT id, goal, status, started_at, completed_at, report_md FROM research_runs WHERE id = ?`
	row := s.db.QueryRow(query, runID)
	var r ResearchRun
	err := row.Scan(&r.ID, &r.Goal, &r.Status, &r.StartedAt, &r.CompletedAt, &r.ReportMD)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRecentResearchRuns retrieves recent deep research runs ordered by most recent first.
func (s *Store) GetRecentResearchRuns(limit int) ([]ResearchRun, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, goal, status, started_at, completed_at, report_md
		FROM research_runs
		ORDER BY id DESC
		LIMIT ?;
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query research runs: %w", err)
	}
	defer rows.Close()

	var runs []ResearchRun
	for rows.Next() {
		var r ResearchRun
		if err := rows.Scan(&r.ID, &r.Goal, &r.Status, &r.StartedAt, &r.CompletedAt, &r.ReportMD); err != nil {
			return nil, fmt.Errorf("failed to scan research run: %w", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating research runs: %w", err)
	}
	return runs, nil
}

func (s *Store) CreateSubQuestion(runID int64, question string) (int64, error) {
	query := `INSERT INTO research_subquestions (run_id, question, status) VALUES (?, ?, 'pending') RETURNING id;`
	var sqID int64
	err := s.db.QueryRow(query, runID, question).Scan(&sqID)
	if err != nil {
		return 0, fmt.Errorf("failed to create subquestion: %w", err)
	}
	return sqID, nil
}

func (s *Store) UpdateSubQuestionStatus(sqID int64, status string) error {
	query := `UPDATE research_subquestions SET status = ? WHERE id = ?;`
	_, err := s.db.Exec(query, status, sqID)
	return err
}

func (s *Store) GetSubQuestionsForRun(runID int64) ([]ResearchSubQuestion, error) {
	query := `SELECT id, run_id, question, status FROM research_subquestions WHERE run_id = ? ORDER BY id ASC`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sqs []ResearchSubQuestion
	for rows.Next() {
		var sq ResearchSubQuestion
		if err := rows.Scan(&sq.ID, &sq.RunID, &sq.Question, &sq.Status); err != nil {
			return nil, err
		}
		sqs = append(sqs, sq)
	}
	return sqs, nil
}

func (s *Store) SaveFinding(sqID int64, claim, sourceURL, sourceProvider string, confidence float64) (int64, error) {
	now := time.Now().UTC()
	query := `INSERT INTO findings (subquestion_id, claim, source_url, source_provider, confidence, created_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id;`
	var fID int64
	err := s.db.QueryRow(query, sqID, claim, sourceURL, sourceProvider, confidence, now).Scan(&fID)
	if err != nil {
		return 0, fmt.Errorf("failed to save finding: %w", err)
	}
	return fID, nil
}

func (s *Store) GetFindingsForSubQuestion(sqID int64) ([]Finding, error) {
	query := `SELECT id, subquestion_id, claim, source_url, source_provider, confidence, created_at FROM findings WHERE subquestion_id = ? ORDER BY id ASC`
	rows, err := s.db.Query(query, sqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fs []Finding
	for rows.Next() {
		var f Finding
		if err := rows.Scan(&f.ID, &f.SubQuestionID, &f.Claim, &f.SourceURL, &f.SourceProvider, &f.Confidence, &f.CreatedAt); err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, nil
}

func (s *Store) GetAllFindingsForRun(runID int64) ([]Finding, error) {
	query := `
		SELECT f.id, f.subquestion_id, f.claim, f.source_url, f.source_provider, f.confidence, f.created_at 
		FROM findings f
		JOIN research_subquestions sq ON f.subquestion_id = sq.id
		WHERE sq.run_id = ?
		ORDER BY f.id ASC
	`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fs []Finding
	for rows.Next() {
		var f Finding
		if err := rows.Scan(&f.ID, &f.SubQuestionID, &f.Claim, &f.SourceURL, &f.SourceProvider, &f.Confidence, &f.CreatedAt); err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, nil
}

// GetStats returns aggregated counts of the database records for the dashboard.
func (s *Store) GetStats() (Stats, error) {
	var stats Stats
	err := s.db.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&stats.PagesScraped)
	if err != nil {
		return stats, fmt.Errorf("failed to count pages: %w", err)
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM extractions`).Scan(&stats.ExtractionsDone)
	if err != nil {
		return stats, fmt.Errorf("failed to count extractions: %w", err)
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_runs`).Scan(&stats.AgentRuns)
	if err != nil {
		return stats, fmt.Errorf("failed to count agent runs: %w", err)
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM research_runs`).Scan(&stats.DeepResearchRuns)
	if err != nil {
		return stats, fmt.Errorf("failed to count research runs: %w", err)
	}
	return stats, nil
}

// GetMergedHistory retrieves a merged and chronologically sorted list of recent agent and research runs.
func (s *Store) GetMergedHistory(limit int) ([]RunHistoryItem, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT * FROM (
			SELECT id, 'agent' as type, goal, status, created_at as started_at
			FROM agent_runs
			UNION ALL
			SELECT id, 'research' as type, goal, status, started_at
			FROM research_runs
		)
		ORDER BY started_at DESC
		LIMIT ?;
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query merged history: %w", err)
	}
	defer rows.Close()

	var history []RunHistoryItem
	for rows.Next() {
		var item RunHistoryItem
		if err := rows.Scan(&item.ID, &item.Type, &item.Goal, &item.Status, &item.StartedAt); err != nil {
			return nil, fmt.Errorf("failed to scan run history item: %w", err)
		}
		history = append(history, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating merged history: %w", err)
	}
	return history, nil
}
