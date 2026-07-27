CREATE TABLE IF NOT EXISTS pages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT UNIQUE,
    fetched_at DATETIME,
    raw_html TEXT,
    clean_text TEXT
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
