// Package store 负责 RAG 独立 sqlite 文件（默认 data/rag.db）的打开与建表。
// 用 database/sql + glebarez/go-sqlite 直连，不上 GORM。
package store

import (
	"database/sql"
	"fmt"

	_ "github.com/glebarez/go-sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS rag_documents (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    source_type  TEXT    NOT NULL DEFAULT 'file',
    path         TEXT    NOT NULL UNIQUE,
    mtime        INTEGER NOT NULL,
    size         INTEGER NOT NULL,
    hash         TEXT    NOT NULL,
    indexed_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rag_chunks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    doc_id     INTEGER NOT NULL REFERENCES rag_documents(id) ON DELETE CASCADE,
    ord        INTEGER NOT NULL,
    content    TEXT    NOT NULL,
    char_start INTEGER NOT NULL,
    char_end   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rag_chunks_doc ON rag_chunks(doc_id);

CREATE TABLE IF NOT EXISTS rag_vec (
    chunk_id  INTEGER PRIMARY KEY REFERENCES rag_chunks(id) ON DELETE CASCADE,
    embedding BLOB    NOT NULL
);
`

// Open 打开 dsn（文件路径或 sqlite DSN），启用外键，建表。
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", dsn, err)
	}
	// SQLite 默认不启外键；建表用了 REFERENCES/ON DELETE CASCADE，
	// 不开这个 PRAGMA 会静默失效。
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: enable foreign_keys: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate schema: %w", err)
	}
	return db, nil
}
