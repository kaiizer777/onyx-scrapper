package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)


//go:embed schema.sql
var schemaSQL string

type Page struct {
	ID             int64     `json:"id"`
	URL            string    `json:"url"`
	FetchedAt      time.Time `json:"fetched_at"`
	RawHTML        string    `json:"raw_html"`
	CleanText      string    `json:"clean_text"`
	SourceProvider string    `json:"source_provider"`
	FetchIntegrity string    `json:"fetch_integrity"`
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
	ID         int64     `json:"id"`
	Goal       string    `json:"goal"`
	Status     string    `json:"status"`
	Result     string    `json:"result"`
	IsGrounded bool      `json:"is_grounded"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
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
	ID        any       `json:"id"`
	Type      string    `json:"type"` // "agent", "research", or "teacher"
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

type FindingStatus string

const (
	StatusActive       FindingStatus = "active"
	StatusContradicted FindingStatus = "contradicted"
	StatusUnclear      FindingStatus = "unclear"
)

type Finding struct {
	ID               int64         `json:"id"`
	SubQuestionID    int64         `json:"subquestion_id,omitempty"`
	AgentRunID       int64         `json:"agent_run_id,omitempty"`
	Claim            string        `json:"claim"`
	SourceURL        string        `json:"source_url"`
	SourceProvider   string        `json:"source_provider"`
	Confidence       float64       `json:"confidence"`
	Status           FindingStatus `json:"status"`
	VerificationNote string        `json:"verification_note"`
	AuthorityTier    int           `json:"authority_tier"`
	CreatedAt        time.Time     `json:"created_at"`
}

// TelegramSession is the Phase-7 join row that links a Telegram chat to
// a single Onyx run (agent_runs or research_runs). One row per chat
// per run; the same chat can have many rows over time. RunID is
// nullable because the gateway creates the row at "Starting..." time,
// before the engine allocates a row of its own; the worker back-fills
// RunID via UpdateTelegramSessionRunID once the engine id is known.
// The AckMessageID field records the message_id of the ack reply so
// the gateway can edit it in place to show progress without spamming
// the chat.
type TelegramSession struct {
	ID           int64     `json:"id"`
	ChatID       int64     `json:"chat_id"`
	RunType      string    `json:"run_type"`     // "agent" or "research"
	RunID        *int64    `json:"run_id"`       // FK into agent_runs.id or research_runs.id; nullable
	Status       string    `json:"status"`       // pending | running | completed | failed | cancelled
	Goal         string    `json:"goal"`
	AckMessageID int       `json:"ack_message_id"`
	LastStep     int       `json:"last_step"`
	LastAction   string    `json:"last_action"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type UserProfile struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProfileField struct {
	ID            int64     `json:"id"`
	ProfileID     int64     `json:"profile_id"`
	FieldName     string    `json:"field_name"`
	KeywordsCSV   string    `json:"keywords_csv"`
	PriorityOrder int       `json:"priority_order"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
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

	// modernc.org/sqlite requires single-writer serialization to prevent SQLITE_BUSY
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Configure SQLite connection pragmas (WAL mode, busy timeout, foreign keys)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=10000; PRAGMA foreign_keys=ON;"); err != nil {
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
	db.Exec("ALTER TABLE findings ADD COLUMN status TEXT NOT NULL DEFAULT 'active';")
	db.Exec("ALTER TABLE findings ADD COLUMN verification_note TEXT NOT NULL DEFAULT '';")
	db.Exec("ALTER TABLE findings ADD COLUMN agent_run_id INTEGER;")
	db.Exec("ALTER TABLE findings ADD COLUMN authority_tier INTEGER NOT NULL DEFAULT 0;")
	db.Exec("ALTER TABLE agent_runs ADD COLUMN is_grounded INTEGER NOT NULL DEFAULT 0;")
	db.Exec("ALTER TABLE pages ADD COLUMN source_provider TEXT;")
	db.Exec("ALTER TABLE pages ADD COLUMN fetch_integrity TEXT NOT NULL DEFAULT 'ok';")
	db.Exec("ALTER TABLE entity_cache ADD COLUMN entity_type TEXT NOT NULL DEFAULT '';")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_entity_cache_typed ON entity_cache(entity, entity_type, version_token);")
	db.Exec("CREATE TABLE IF NOT EXISTS run_pages (run_id INTEGER, url TEXT, FOREIGN KEY(run_id) REFERENCES research_runs(id) ON DELETE CASCADE, UNIQUE(run_id, url));")
	// Phase 7: Telegram session linking. The schema.sql block already
	// creates the table for fresh databases; this is the no-op upgrade
	// path for existing ones. CREATE TABLE IF NOT EXISTS is idempotent.
	db.Exec("CREATE TABLE IF NOT EXISTS telegram_sessions (id INTEGER PRIMARY KEY AUTOINCREMENT, chat_id INTEGER NOT NULL, run_type TEXT NOT NULL, run_id INTEGER, status TEXT NOT NULL, goal TEXT, ack_message_id INTEGER, last_step INTEGER NOT NULL DEFAULT 0, last_action TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL);")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_telegram_sessions_chat ON telegram_sessions(chat_id, id DESC);")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_telegram_sessions_status ON telegram_sessions(status);")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_sessions_run ON telegram_sessions(run_type, run_id) WHERE run_id IS NOT NULL;")

	// Profile tables migration
	db.Exec("CREATE TABLE IF NOT EXISTS user_profiles (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL);")
	db.Exec("CREATE TABLE IF NOT EXISTS profile_fields (id INTEGER PRIMARY KEY AUTOINCREMENT, profile_id INTEGER NOT NULL, field_name TEXT NOT NULL, keywords_csv TEXT NOT NULL, priority_order INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL, FOREIGN KEY(profile_id) REFERENCES user_profiles(id) ON DELETE CASCADE, UNIQUE(profile_id, field_name));")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_profile_fields_profile ON profile_fields(profile_id, priority_order ASC);")

	// Teacher tables migration
	db.Exec("CREATE TABLE IF NOT EXISTS teacher_runs (id TEXT PRIMARY KEY, raw_goal TEXT NOT NULL, status TEXT NOT NULL, learning_brief TEXT, report_md TEXT, error_message TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, completed_at DATETIME);")
	db.Exec("CREATE TABLE IF NOT EXISTS teacher_clarifications (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES teacher_runs(id) ON DELETE CASCADE, round INTEGER NOT NULL, question TEXT NOT NULL, answer TEXT, created_at DATETIME NOT NULL);")
	db.Exec("CREATE TABLE IF NOT EXISTS teacher_outline (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES teacher_runs(id) ON DELETE CASCADE, section_order INTEGER NOT NULL, title TEXT NOT NULL, learning_objective TEXT NOT NULL, depends_on TEXT, status TEXT NOT NULL);")
	db.Exec("CREATE TABLE IF NOT EXISTS teacher_findings (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES teacher_runs(id) ON DELETE CASCADE, section_id TEXT NOT NULL REFERENCES teacher_outline(id) ON DELETE CASCADE, claim TEXT NOT NULL, source_url TEXT, source_provider TEXT, authority_tier TEXT, confidence REAL, created_at DATETIME NOT NULL);")
	db.Exec("CREATE TABLE IF NOT EXISTS teacher_sections (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES teacher_runs(id) ON DELETE CASCADE, outline_id TEXT NOT NULL REFERENCES teacher_outline(id) ON DELETE CASCADE, draft_md TEXT, critique_notes TEXT, final_md TEXT, revision_count INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL);")
	db.Exec("CREATE VIRTUAL TABLE IF NOT EXISTS teacher_fts USING fts5(run_id, section_title, content);")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_teacher_clarifications_run ON teacher_clarifications(run_id, round ASC);")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_teacher_outline_run ON teacher_outline(run_id, section_order ASC);")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_teacher_findings_run_sec ON teacher_findings(run_id, section_id);")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_teacher_sections_run ON teacher_sections(run_id, outline_id);")

	return &Store{db: db}, nil
}

// DB returns the underlying sql.DB connection.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// SavePage inserts or updates a page record by URL and returns its database ID.
func (s *Store) SavePage(url, rawHTML, cleanText, sourceProvider string, fetchIntegrity string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()

	query := `
		INSERT INTO pages (url, fetched_at, raw_html, clean_text, source_provider, fetch_integrity)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET
			fetched_at = excluded.fetched_at,
			raw_html = excluded.raw_html,
			clean_text = excluded.clean_text,
			source_provider = excluded.source_provider,
			fetch_integrity = excluded.fetch_integrity
		RETURNING id;
	`

	var pageID int64
	err := s.db.QueryRow(query, url, now, rawHTML, cleanText, sourceProvider, fetchIntegrity).Scan(&pageID)
	if err != nil {
		return 0, fmt.Errorf("failed to save page (%s): %w", url, err)
	}

	return pageID, nil
}

// GetPageByURL retrieves a saved page by its exact URL.
func (s *Store) GetPageByURL(url string) (*Page, error) {
	query := `SELECT id, url, fetched_at, raw_html, clean_text, source_provider, fetch_integrity FROM pages WHERE url = ?`
	row := s.db.QueryRow(query, url)

	var p Page
	err := row.Scan(&p.ID, &p.URL, &p.FetchedAt, &p.RawHTML, &p.CleanText, &p.SourceProvider, &p.FetchIntegrity)
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
	query := `SELECT id, url, fetched_at, raw_html, clean_text, source_provider, fetch_integrity FROM pages WHERE id = ?`
	row := s.db.QueryRow(query, id)

	var p Page
	err := row.Scan(&p.ID, &p.URL, &p.FetchedAt, &p.RawHTML, &p.CleanText, &p.SourceProvider, &p.FetchIntegrity)
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
		SELECT id, url, fetched_at, raw_html, clean_text, source_provider, fetch_integrity 
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
		if err := rows.Scan(&p.ID, &p.URL, &p.FetchedAt, &p.RawHTML, &p.CleanText, &p.SourceProvider, &p.FetchIntegrity); err != nil {
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
	query := `SELECT id, goal, status, result, COALESCE(is_grounded, 0), created_at, updated_at FROM agent_runs WHERE id = ?`
	row := s.db.QueryRow(query, runID)

	var run AgentRun
	var isGroundedInt int
	err := row.Scan(&run.ID, &run.Goal, &run.Status, &run.Result, &isGroundedInt, &run.CreatedAt, &run.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get agent run %d: %w", runID, err)
	}
	run.IsGrounded = isGroundedInt != 0
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
		SELECT id, goal, status, result, COALESCE(is_grounded, 0), created_at, updated_at
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
		var isGroundedInt int
		if err := rows.Scan(&r.ID, &r.Goal, &r.Status, &r.Result, &isGroundedInt, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan agent run: %w", err)
		}
		r.IsGrounded = isGroundedInt != 0
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating agent runs: %w", err)
	}
	return runs, nil
}

// AddPageToRun associates a fetched page with a research run.
func (s *Store) AddPageToRun(runID int64, url string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.Exec(`INSERT OR IGNORE INTO run_pages (run_id, url) VALUES (?, ?)`, runID, url)
	return err
}

// GetPagesForRun retrieves all unique pages fetched during a research run.
func (s *Store) GetPagesForRun(runID int64) ([]Page, error) {
	query := `
		SELECT p.id, p.url, p.fetched_at, p.raw_html, p.clean_text, p.source_provider, p.fetch_integrity
		FROM pages p
		JOIN run_pages rp ON p.url = rp.url
		WHERE rp.run_id = ?
	`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query run pages: %w", err)
	}
	defer rows.Close()

	var pages []Page
	for rows.Next() {
		var p Page
		if err := rows.Scan(&p.ID, &p.URL, &p.FetchedAt, &p.RawHTML, &p.CleanText, &p.SourceProvider, &p.FetchIntegrity); err != nil {
			return nil, fmt.Errorf("failed to scan page: %w", err)
		}
		pages = append(pages, p)
	}
	return pages, nil
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

func (s *Store) InsertFinding(f Finding) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := f.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	status := f.Status
	if status == "" {
		status = StatusActive
	}
	var sqIDVal any = f.SubQuestionID
	if f.SubQuestionID <= 0 {
		sqIDVal = nil
	}
	var agentRunIDVal any = f.AgentRunID
	if f.AgentRunID <= 0 {
		agentRunIDVal = nil
	}
	query := `INSERT INTO findings (subquestion_id, agent_run_id, claim, source_url, source_provider, confidence, status, verification_note, authority_tier, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id;`
	var fID int64
	err := s.db.QueryRow(query, sqIDVal, agentRunIDVal, f.Claim, f.SourceURL, f.SourceProvider, f.Confidence, string(status), f.VerificationNote, f.AuthorityTier, now).Scan(&fID)
	if err != nil {
		return 0, fmt.Errorf("failed to insert finding: %w", err)
	}
	return fID, nil
}

func (s *Store) SaveFinding(sqID int64, claim, sourceURL, sourceProvider string, confidence float64) (int64, error) {
	return s.InsertFinding(Finding{
		SubQuestionID:  sqID,
		Claim:          claim,
		SourceURL:      sourceURL,
		SourceProvider: sourceProvider,
		Confidence:     confidence,
		Status:         StatusActive,
	})
}

func (s *Store) GetFindingsForSubQuestion(sqID int64) ([]Finding, error) {
	query := `SELECT id, COALESCE(subquestion_id, 0), COALESCE(agent_run_id, 0), claim, source_url, COALESCE(source_provider, ''), confidence, COALESCE(status, 'active'), COALESCE(verification_note, ''), COALESCE(authority_tier, 0), created_at FROM findings WHERE subquestion_id = ? ORDER BY id ASC`
	rows, err := s.db.Query(query, sqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fs []Finding
	for rows.Next() {
		var f Finding
		var statusStr, noteStr string
		if err := rows.Scan(&f.ID, &f.SubQuestionID, &f.AgentRunID, &f.Claim, &f.SourceURL, &f.SourceProvider, &f.Confidence, &statusStr, &noteStr, &f.AuthorityTier, &f.CreatedAt); err != nil {
			return nil, err
		}
		if statusStr == "" {
			f.Status = StatusActive
		} else {
			f.Status = FindingStatus(statusStr)
		}
		f.VerificationNote = noteStr
		fs = append(fs, f)
	}
	return fs, nil
}

func (s *Store) GetAllFindingsForRun(runID int64) ([]Finding, error) {
	query := `
		SELECT f.id, COALESCE(f.subquestion_id, 0), COALESCE(f.agent_run_id, 0), f.claim, f.source_url, COALESCE(f.source_provider, ''), f.confidence, COALESCE(f.status, 'active'), COALESCE(f.verification_note, ''), COALESCE(f.authority_tier, 0), f.created_at 
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
		var statusStr, noteStr string
		if err := rows.Scan(&f.ID, &f.SubQuestionID, &f.AgentRunID, &f.Claim, &f.SourceURL, &f.SourceProvider, &f.Confidence, &statusStr, &noteStr, &f.AuthorityTier, &f.CreatedAt); err != nil {
			return nil, err
		}
		if statusStr == "" {
			f.Status = StatusActive
		} else {
			f.Status = FindingStatus(statusStr)
		}
		f.VerificationNote = noteStr
		fs = append(fs, f)
	}
	return fs, nil
}

// GetFindingsByAgentRun retrieves all findings recorded during an agent run.
func (s *Store) GetFindingsByAgentRun(runID int64) ([]Finding, error) {
	query := `
		SELECT id, COALESCE(subquestion_id, 0), COALESCE(agent_run_id, 0), claim, source_url, COALESCE(source_provider, ''), confidence, COALESCE(status, 'active'), COALESCE(verification_note, ''), COALESCE(authority_tier, 0), created_at 
		FROM findings 
		WHERE agent_run_id = ? 
		ORDER BY id ASC
	`
	rows, err := s.db.Query(query, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to query findings for agent run %d: %w", runID, err)
	}
	defer rows.Close()
	var fs []Finding
	for rows.Next() {
		var f Finding
		var statusStr, noteStr string
		if err := rows.Scan(&f.ID, &f.SubQuestionID, &f.AgentRunID, &f.Claim, &f.SourceURL, &f.SourceProvider, &f.Confidence, &statusStr, &noteStr, &f.AuthorityTier, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan finding: %w", err)
		}
		if statusStr == "" {
			f.Status = StatusActive
		} else {
			f.Status = FindingStatus(statusStr)
		}
		f.VerificationNote = noteStr
		fs = append(fs, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating findings: %w", err)
	}
	return fs, nil
}

// MarkAgentRunGrounded marks the agent run as grounded and updates updated_at.
func (s *Store) MarkAgentRunGrounded(runID int64) error {
	now := time.Now().UTC()
	query := `UPDATE agent_runs SET is_grounded = 1, updated_at = ? WHERE id = ?;`
	_, err := s.db.Exec(query, now, runID)
	if err != nil {
		return fmt.Errorf("failed to mark agent run %d as grounded: %w", runID, err)
	}
	return nil
}

// UpdateFindingStatusAndNote updates the verification status and note for a finding.
func (s *Store) UpdateFindingStatusAndNote(findingID int64, status FindingStatus, note string) error {
	query := `UPDATE findings SET status = ?, verification_note = ? WHERE id = ?;`
	_, err := s.db.Exec(query, string(status), note, findingID)
	if err != nil {
		return fmt.Errorf("failed to update finding %d status: %w", findingID, err)
	}
	return nil
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

// GetMergedHistory retrieves a merged and chronologically sorted list of recent
// agent and research runs. The sidebar in the /ui page renders both
// types (the "type" discriminator tells the front-end which renderer to use).
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
			UNION ALL
			SELECT id, 'teacher' as type, raw_goal as goal, status, created_at as started_at
			FROM teacher_runs
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
		var rawID any
		if err := rows.Scan(&rawID, &item.Type, &item.Goal, &item.Status, &item.StartedAt); err != nil {
			return nil, fmt.Errorf("failed to scan run history item: %w", err)
		}
		switch v := rawID.(type) {
		case []byte:
			item.ID = string(v)
		case string:
			item.ID = v
		case int64:
			item.ID = v
		default:
			item.ID = fmt.Sprintf("%v", v)
		}
		history = append(history, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating merged history: %w", err)
	}
	return history, nil
}

// GetEntityCache retrieves the verified result from cache if it's within TTL.
func (s *Store) GetEntityCache(entity, entityType, token string, ttlHours int) (string, string, bool) {
	if ttlHours <= 0 {
		ttlHours = 24
	}
	query := `SELECT result, value, created_at FROM entity_cache WHERE entity = ? AND entity_type = ? AND version_token = ?`
	row := s.db.QueryRow(query, entity, entityType, token)
	var result, value string
	var createdAt time.Time
	if err := row.Scan(&result, &value, &createdAt); err != nil {
		return "", "", false
	}
	if time.Since(createdAt).Hours() > float64(ttlHours) {
		return "", "", false // expired
	}
	return result, value, true
}

// SaveEntityCache saves the verified result to cache.
func (s *Store) SaveEntityCache(entity, entityType, token, result, value string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	query := `INSERT INTO entity_cache (entity, entity_type, version_token, result, value, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(entity, entity_type, version_token) DO UPDATE SET result=excluded.result, value=excluded.value, created_at=excluded.created_at;`
	_, err := s.db.Exec(query, entity, entityType, token, result, value, now)
	return err
}

// CreateTelegramSession inserts a new Phase-7 join row. runID is the
// engine-side row id (agent_runs.id or research_runs.id) the worker
// will create or resume; pass nil to create a "pending" row that the
// worker back-fills via UpdateTelegramSessionRunID. Status starts as
// "pending" — the caller flips it to "running" once the worker
// actually starts. Returns the new row id.
func (s *Store) CreateTelegramSession(chatID int64, runType string, runID *int64, goal string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	query := `
		INSERT INTO telegram_sessions
			(chat_id, run_type, run_id, status, goal, ack_message_id, last_step, last_action, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, 0, 0, '', ?, ?)
		RETURNING id;
	`
	var id int64
	err := s.db.QueryRow(query, chatID, runType, runID, goal, now, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to create telegram session (chat %d, %s): %w", chatID, runType, err)
	}
	return id, nil
}

// UpdateTelegramSessionRunID back-fills the engine-side row id once
// the worker has allocated it. Called from the agent / research
// worker goroutine after Run() returns its AgentRun / ResearchRun.
func (s *Store) UpdateTelegramSessionRunID(id int64, runID int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	query := `UPDATE telegram_sessions SET run_id = ?, updated_at = ? WHERE id = ?;`
	if _, err := s.db.Exec(query, runID, now, id); err != nil {
		return fmt.Errorf("failed to back-fill telegram session %d run_id: %w", id, err)
	}
	return nil
}

// UpdateTelegramSessionStatus is the generic status flip used by the
// gateway lifecycle. The optional ackMessageID/step/action are only
// applied when non-zero / non-empty — pass zero values to update only
// the status.
func (s *Store) UpdateTelegramSessionStatus(id int64, status string, ackMessageID int, lastStep int, lastAction string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	// We build the SET clause dynamically so callers that only want to
	// flip status do not clobber the ack_message_id / progress fields.
	sets := []string{"status = ?", "updated_at = ?"}
	args := []interface{}{status, now}
	if ackMessageID > 0 {
		sets = append(sets, "ack_message_id = ?")
		args = append(args, ackMessageID)
	}
	if lastStep > 0 {
		sets = append(sets, "last_step = ?")
		args = append(args, lastStep)
	}
	if lastAction != "" {
		sets = append(sets, "last_action = ?")
		args = append(args, lastAction)
	}
	args = append(args, id)
	query := "UPDATE telegram_sessions SET " + strings.Join(sets, ", ") + " WHERE id = ?;"
	if _, err := s.db.Exec(query, args...); err != nil {
		return fmt.Errorf("failed to update telegram session %d: %w", id, err)
	}
	return nil
}

// UpdateTelegramSessionProgress is the hot path called from the
// agent/research StepCallback. It updates last_step + last_action in
// one round-trip; status is left alone. We keep this separate from the
// generic status updater so the per-step call is a single, narrow SQL
// statement (cheap on the poller's hot loop).
func (s *Store) UpdateTelegramSessionProgress(id int64, lastStep int, lastAction string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	query := `UPDATE telegram_sessions SET last_step = ?, last_action = ?, updated_at = ? WHERE id = ?;`
	if _, err := s.db.Exec(query, lastStep, lastAction, now, id); err != nil {
		return fmt.Errorf("failed to update telegram session progress (id %d): %w", id, err)
	}
	return nil
}

// GetActiveTelegramSessionCount returns the number of sessions currently in 'running' state.
func (s *Store) GetActiveTelegramSessionCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT count(*) FROM telegram_sessions WHERE status = 'running';`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count active telegram sessions: %w", err)
	}
	return count, nil
}

// GetLatestTelegramSession returns the most recently created session
// row for a chat, regardless of status. Used by /status.
func (s *Store) GetLatestTelegramSession(chatID int64) (*TelegramSession, error) {
	query := `
		SELECT id, chat_id, run_type, run_id, status, goal, ack_message_id, last_step, last_action, created_at, updated_at
		FROM telegram_sessions
		WHERE chat_id = ?
		ORDER BY id DESC
		LIMIT 1;
	`
	row := s.db.QueryRow(query, chatID)
	sess, err := scanTelegramSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load latest telegram session for chat %d: %w", chatID, err)
	}
	return sess, nil
}

// GetTelegramSessionByRun looks up a session row by its (run_type, run_id)
// key. Returns nil if no link exists (which is normal for runs that
// were not started from a Telegram chat).
func (s *Store) GetTelegramSessionByRun(runType string, runID int64) (*TelegramSession, error) {
	query := `
		SELECT id, chat_id, run_type, run_id, status, goal, ack_message_id, last_step, last_action, created_at, updated_at
		FROM telegram_sessions
		WHERE run_type = ? AND run_id = ?
		LIMIT 1;
	`
	row := s.db.QueryRow(query, runType, runID)
	sess, err := scanTelegramSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load telegram session for %s run %d: %w", runType, runID, err)
	}
	return sess, nil
}

// ListTelegramSessionsForChat returns the N most recent session rows
// for a chat, newest first. Used by an optional /history command
// (Phase 7 stretch goal) and for tests.
func (s *Store) ListTelegramSessionsForChat(chatID int64, limit int) ([]TelegramSession, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT id, chat_id, run_type, run_id, status, goal, ack_message_id, last_step, last_action, created_at, updated_at
		FROM telegram_sessions
		WHERE chat_id = ?
		ORDER BY id DESC
		LIMIT ?;
	`
	rows, err := s.db.Query(query, chatID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query telegram sessions for chat %d: %w", chatID, err)
	}
	defer rows.Close()

	var out []TelegramSession
	for rows.Next() {
		sess, err := scanTelegramSession(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan telegram session: %w", err)
		}
		out = append(out, *sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating telegram sessions: %w", err)
	}
	return out, nil
}

// scanTelegramSession factors out the NULL-handling for the nullable
// run_id column. Accepts anything that satisfies Scan(...interface{}).
func scanTelegramSession(s scanner) (*TelegramSession, error) {
	var (
		sess      TelegramSession
		runIDNull sql.NullInt64
	)
	err := s.Scan(&sess.ID, &sess.ChatID, &sess.RunType, &runIDNull, &sess.Status,
		&sess.Goal, &sess.AckMessageID, &sess.LastStep, &sess.LastAction, &sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if runIDNull.Valid {
		v := runIDNull.Int64
		sess.RunID = &v
	}
	return &sess, nil
}

// scanner is the small interface satisfied by both *sql.Row and
// *sql.Rows. Letting scanTelegramSession take it lets the same
// function serve QueryRow and Query paths.
type scanner interface {
	Scan(dest ...interface{}) error
}

// CreateProfile inserts a new user profile record.
func (s *Store) CreateProfile(name string) (*UserProfile, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	query := `INSERT INTO user_profiles (name, created_at, updated_at) VALUES (?, ?, ?) RETURNING id;`
	var id int64
	if err := s.db.QueryRow(query, name, now, now).Scan(&id); err != nil {
		return nil, fmt.Errorf("failed to create user profile %q: %w", name, err)
	}
	return &UserProfile{
		ID:        id,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// GetProfile retrieves a profile by ID.
func (s *Store) GetProfile(id int64) (*UserProfile, error) {
	query := `SELECT id, name, created_at, updated_at FROM user_profiles WHERE id = ?;`
	row := s.db.QueryRow(query, id)
	var p UserProfile
	err := row.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get profile %d: %w", id, err)
	}
	return &p, nil
}

// GetProfileByName retrieves a profile by exact name.
func (s *Store) GetProfileByName(name string) (*UserProfile, error) {
	query := `SELECT id, name, created_at, updated_at FROM user_profiles WHERE name = ?;`
	row := s.db.QueryRow(query, name)
	var p UserProfile
	err := row.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get profile by name %q: %w", name, err)
	}
	return &p, nil
}

// ListProfiles retrieves all user profiles ordered by ID ASC.
func (s *Store) ListProfiles() ([]UserProfile, error) {
	query := `SELECT id, name, created_at, updated_at FROM user_profiles ORDER BY id ASC;`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list profiles: %w", err)
	}
	defer rows.Close()

	var profiles []UserProfile
	for rows.Next() {
		var p UserProfile
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan profile: %w", err)
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

// UpdateProfile renames a user profile.
func (s *Store) UpdateProfile(id int64, name string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	query := `UPDATE user_profiles SET name = ?, updated_at = ? WHERE id = ?;`
	res, err := s.db.Exec(query, name, now, id)
	if err != nil {
		return fmt.Errorf("failed to update profile %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("profile %d not found", id)
	}
	return nil
}

// DeleteProfile deletes a profile and cascades delete to profile_fields.
func (s *Store) DeleteProfile(id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.Exec(`DELETE FROM user_profiles WHERE id = ?;`, id)
	if err != nil {
		return fmt.Errorf("failed to delete profile %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("profile %d not found", id)
	}
	return nil
}

// CreateProfileField creates a new profile field under a profile.
func (s *Store) CreateProfileField(field ProfileField) (*ProfileField, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	enabledInt := 0
	if field.Enabled {
		enabledInt = 1
	}

	query := `
		INSERT INTO profile_fields (profile_id, field_name, keywords_csv, priority_order, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id;
	`
	var id int64
	err := s.db.QueryRow(query, field.ProfileID, field.FieldName, field.KeywordsCSV, field.PriorityOrder, enabledInt, now).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("failed to create profile field %q for profile %d: %w", field.FieldName, field.ProfileID, err)
	}

	field.ID = id
	field.CreatedAt = now
	return &field, nil
}

// GetProfileField retrieves a single profile field by ID.
func (s *Store) GetProfileField(id int64) (*ProfileField, error) {
	query := `SELECT id, profile_id, field_name, keywords_csv, priority_order, enabled, created_at FROM profile_fields WHERE id = ?;`
	row := s.db.QueryRow(query, id)
	var f ProfileField
	var enabledInt int
	err := row.Scan(&f.ID, &f.ProfileID, &f.FieldName, &f.KeywordsCSV, &f.PriorityOrder, &enabledInt, &f.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get profile field %d: %w", id, err)
	}
	f.Enabled = (enabledInt != 0)
	return &f, nil
}

// UpdateProfileField updates an existing profile field.
func (s *Store) UpdateProfileField(field ProfileField) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	enabledInt := 0
	if field.Enabled {
		enabledInt = 1
	}

	query := `
		UPDATE profile_fields
		SET field_name = ?, keywords_csv = ?, priority_order = ?, enabled = ?
		WHERE id = ?;
	`
	res, err := s.db.Exec(query, field.FieldName, field.KeywordsCSV, field.PriorityOrder, enabledInt, field.ID)
	if err != nil {
		return fmt.Errorf("failed to update profile field %d: %w", field.ID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("profile field %d not found", field.ID)
	}
	return nil
}

// DeleteProfileField deletes a profile field by ID.
func (s *Store) DeleteProfileField(id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.Exec(`DELETE FROM profile_fields WHERE id = ?;`, id)
	if err != nil {
		return fmt.Errorf("failed to delete profile field %d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("profile field %d not found", id)
	}
	return nil
}

// ListProfileFields retrieves all fields for a profile ordered by priority_order ASC, id ASC.
func (s *Store) ListProfileFields(profileID int64) ([]ProfileField, error) {
	query := `
		SELECT id, profile_id, field_name, keywords_csv, priority_order, enabled, created_at
		FROM profile_fields
		WHERE profile_id = ?
		ORDER BY priority_order ASC, id ASC;
	`
	rows, err := s.db.Query(query, profileID)
	if err != nil {
		return nil, fmt.Errorf("failed to list profile fields for profile %d: %w", profileID, err)
	}
	defer rows.Close()

	var fields []ProfileField
	for rows.Next() {
		var f ProfileField
		var enabledInt int
		if err := rows.Scan(&f.ID, &f.ProfileID, &f.FieldName, &f.KeywordsCSV, &f.PriorityOrder, &enabledInt, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan profile field: %w", err)
		}
		f.Enabled = (enabledInt != 0)
		fields = append(fields, f)
	}
	return fields, nil
}

// ReplaceProfileFields replaces all fields for a profile in a single transaction.
func (s *Store) ReplaceProfileFields(profileID int64, fields []ProfileField) ([]ProfileField, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin tx for ReplaceProfileFields: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM profile_fields WHERE profile_id = ?;`, profileID); err != nil {
		return nil, fmt.Errorf("failed to clear profile fields: %w", err)
	}

	now := time.Now().UTC()
	var result []ProfileField

	stmt, err := tx.Prepare(`
		INSERT INTO profile_fields (profile_id, field_name, keywords_csv, priority_order, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id;
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare insert stmt: %w", err)
	}
	defer stmt.Close()

	for i, f := range fields {
		enabledInt := 0
		if f.Enabled {
			enabledInt = 1
		}
		pOrder := f.PriorityOrder
		if pOrder == 0 {
			pOrder = i + 1
		}

		var id int64
		if err := stmt.QueryRow(profileID, f.FieldName, f.KeywordsCSV, pOrder, enabledInt, now).Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to insert profile field %q: %w", f.FieldName, err)
		}

		f.ID = id
		f.ProfileID = profileID
		f.PriorityOrder = pOrder
		f.CreatedAt = now
		result = append(result, f)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit ReplaceProfileFields tx: %w", err)
	}

	return result, nil
}

// ListEnabledProfileFields retrieves enabled fields for a profile ordered by priority_order ASC, id ASC.
func (s *Store) ListEnabledProfileFields(profileID int64) ([]ProfileField, error) {
	query := `
		SELECT id, profile_id, field_name, keywords_csv, priority_order, enabled, created_at
		FROM profile_fields
		WHERE profile_id = ? AND enabled = 1
		ORDER BY priority_order ASC, id ASC;
	`
	rows, err := s.db.Query(query, profileID)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled profile fields for profile %d: %w", profileID, err)
	}
	defer rows.Close()

	var fields []ProfileField
	for rows.Next() {
		var f ProfileField
		var enabledInt int
		if err := rows.Scan(&f.ID, &f.ProfileID, &f.FieldName, &f.KeywordsCSV, &f.PriorityOrder, &enabledInt, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan profile field: %w", err)
		}
		f.Enabled = (enabledInt != 0)
		fields = append(fields, f)
	}
	return fields, nil
}

// CountProfileFields returns the total number of fields configured for a profile.
func (s *Store) CountProfileFields(profileID int64) (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM profile_fields WHERE profile_id = ?;`
	if err := s.db.QueryRow(query, profileID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count profile fields for profile %d: %w", profileID, err)
	}
	return count, nil
}
