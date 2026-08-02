CREATE TABLE IF NOT EXISTS pages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT UNIQUE,
    fetched_at DATETIME,
    raw_html TEXT,
    clean_text TEXT,
    source_provider TEXT,
    fetch_integrity TEXT NOT NULL DEFAULT 'ok'
);

CREATE TABLE IF NOT EXISTS extractions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    page_id INTEGER,
    schema_name TEXT,
    data_json TEXT,
    created_at DATETIME,
    FOREIGN KEY(page_id) REFERENCES pages(id) ON DELETE CASCADE
);

CREATE VIRTUAL TABLE IF NOT EXISTS pages_fts USING fts5(
    url,
    clean_text,
    content='pages',
    content_rowid='id'
);

-- Triggers to keep FTS5 synchronized with pages table
CREATE TRIGGER IF NOT EXISTS pages_ai AFTER INSERT ON pages BEGIN
    INSERT INTO pages_fts(rowid, url, clean_text) VALUES (new.id, new.url, new.clean_text);
END;

CREATE TRIGGER IF NOT EXISTS pages_ad AFTER DELETE ON pages BEGIN
    INSERT INTO pages_fts(pages_fts, rowid, url, clean_text) VALUES('delete', old.id, old.url, old.clean_text);
END;

CREATE TRIGGER IF NOT EXISTS pages_au AFTER UPDATE ON pages BEGIN
    INSERT INTO pages_fts(pages_fts, rowid, url, clean_text) VALUES('delete', old.id, old.url, old.clean_text);
    INSERT INTO pages_fts(rowid, url, clean_text) VALUES (new.id, new.url, new.clean_text);
END;

CREATE TABLE IF NOT EXISTS agent_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    goal TEXT,
    status TEXT,
    result TEXT,
    created_at DATETIME,
    updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS agent_steps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER,
    step_number INTEGER,
    step_kind TEXT,
    action TEXT,
    args_json TEXT,
    result TEXT,
    error TEXT,
    created_at DATETIME,
    FOREIGN KEY(run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS research_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    goal TEXT,
    status TEXT,
    started_at DATETIME,
    completed_at DATETIME,
    report_md TEXT
);

CREATE TABLE IF NOT EXISTS research_subquestions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER,
    question TEXT,
    status TEXT,
    FOREIGN KEY(run_id) REFERENCES research_runs(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS run_pages (
    run_id INTEGER,
    url TEXT,
    FOREIGN KEY(run_id) REFERENCES research_runs(id) ON DELETE CASCADE,
    UNIQUE(run_id, url)
);

CREATE TABLE IF NOT EXISTS findings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subquestion_id INTEGER,
    claim TEXT,
    source_url TEXT,
    source_provider TEXT,
    confidence REAL,
    created_at DATETIME,
    FOREIGN KEY(subquestion_id) REFERENCES research_subquestions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS entity_cache (
    entity TEXT,
    version_token TEXT,
    result TEXT,
    value TEXT,
    created_at DATETIME,
    UNIQUE(entity, version_token)
);

-- Phase 7: Telegram gateway session linking. Each row joins a single
-- Telegram chat to a single Onyx run (agent_runs or research_runs).
-- Kept as a join table rather than a chat_id column on the run tables
-- so future run types (scheduled runs, crawl runs, etc.) can link to
-- the same chat without schema churn. run_id is nullable so the
-- gateway can claim a chat slot *before* the engine allocates a
-- engine-side row, then back-fill the link once it knows the id.
CREATE TABLE IF NOT EXISTS telegram_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id INTEGER NOT NULL,
    run_type TEXT NOT NULL,           -- 'agent' or 'research'
    run_id INTEGER,                   -- FK into agent_runs.id or research_runs.id; nullable
    status TEXT NOT NULL,             -- 'pending' | 'running' | 'completed' | 'failed' | 'cancelled'
    goal TEXT,
    ack_message_id INTEGER,           -- Telegram message_id of the ack reply we are editing for progress
    last_step INTEGER NOT NULL DEFAULT 0,
    last_action TEXT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_telegram_sessions_chat ON telegram_sessions(chat_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_telegram_sessions_status ON telegram_sessions(status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_sessions_run ON telegram_sessions(run_type, run_id) WHERE run_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS user_profiles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS profile_fields (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id INTEGER NOT NULL,
    field_name TEXT NOT NULL,
    keywords_csv TEXT NOT NULL,
    priority_order INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL,
    FOREIGN KEY(profile_id) REFERENCES user_profiles(id) ON DELETE CASCADE,
    UNIQUE(profile_id, field_name)
);

CREATE INDEX IF NOT EXISTS idx_profile_fields_profile ON profile_fields(profile_id, priority_order ASC);

