package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type Page struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	FetchedAt time.Time `json:"fetched_at"`
	RawHTML   string    `json:"raw_html"`
	CleanText string    `json:"clean_text"`
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

type Store struct {
	db *sql.DB
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

	// Configure SQLite connection pragmas
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set pragmas: %w", err)
	}

	// Apply schema
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to execute schema: %w", err)
	}

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
func (s *Store) SavePage(url, rawHTML, cleanText string) (int64, error) {
	now := time.Now().UTC()

	query := `
		INSERT INTO pages (url, fetched_at, raw_html, clean_text)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET
			fetched_at = excluded.fetched_at,
			raw_html = excluded.raw_html,
			clean_text = excluded.clean_text
		RETURNING id;
	`

	var pageID int64
	err := s.db.QueryRow(query, url, now, rawHTML, cleanText).Scan(&pageID)
	if err != nil {
		return 0, fmt.Errorf("failed to save page (%s): %w", url, err)
	}

	return pageID, nil
}

// GetPageByURL retrieves a saved page by its exact URL.
func (s *Store) GetPageByURL(url string) (*Page, error) {
	query := `SELECT id, url, fetched_at, raw_html, clean_text FROM pages WHERE url = ?`
	row := s.db.QueryRow(query, url)

	var p Page
	err := row.Scan(&p.ID, &p.URL, &p.FetchedAt, &p.RawHTML, &p.CleanText)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get page by url %s: %w", url, err)
	}

	return &p, nil
}

// SaveExtraction inserts a structured JSON extraction result linked to a page ID.
func (s *Store) SaveExtraction(pageID int64, schemaName, dataJSON string) (int64, error) {
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
