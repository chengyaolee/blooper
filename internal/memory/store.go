package memory

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(dbPath string) (*Store, error) {
	if dbPath == "" {
		dbPath = "data/blooper.db"
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	// ponytail: single connection serializes all writes (no SQLITE_BUSY between concurrent agents);
	// switch to WAL + busy_timeout if read throughput ever matters.
	db.SetMaxOpenConns(1)

	// Create schema
	schema := `
	CREATE TABLE IF NOT EXISTS conversations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_name TEXT NOT NULL,
		user_query TEXT NOT NULL,
		summary TEXT NOT NULL,
		outcome TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		metadata TEXT
	);
	CREATE VIRTUAL TABLE IF NOT EXISTS conversations_fts USING fts5(
		user_query, summary, metadata, content='conversations', content_rowid='id'
	);
	` + taskGraphSchema + traceSchema
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}

	return &Store{db: db}, nil
}

func (s *Store) SaveConversation(agentName, userQuery, summary, outcome, metadata string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO conversations (agent_name, user_query, summary, outcome, metadata)
		VALUES (?, ?, ?, ?, ?)
	`, agentName, userQuery, summary, outcome, metadata)
	if err != nil {
		return fmt.Errorf("failed to insert conversation: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get last insert id: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO conversations_fts (rowid, user_query, summary, metadata)
		VALUES (?, ?, ?, ?)
	`, id, userQuery, summary, metadata)
	if err != nil {
		return fmt.Errorf("failed to insert into fts: %w", err)
	}

	return tx.Commit()
}

func (s *Store) Close() error {
	return s.db.Close()
}
