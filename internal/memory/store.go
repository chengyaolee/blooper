package memory

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/chengyaolee/ragout"
	"github.com/chengyaolee/ragout/chunker"
	"github.com/chengyaolee/ragout/embedder"
	"github.com/chengyaolee/ragout/reader"
	ragstore "github.com/chengyaolee/ragout/store"
	_ "modernc.org/sqlite"
)

// Store keeps past conversations. SQLite holds the records; all indexing and search
// (BM25 + vector, fused by RRF) is delegated to ragout. The index is snapshotted next to
// the database and rebuilt from the records if the snapshot is missing.
type Store struct {
	db   *sql.DB
	rag  *ragout.Engine
	vec  *ragstore.VectorStore
	bm25 *ragstore.BM25Index

	vecPath, bm25Path string
	snapMu            sync.Mutex // serializes snapshot writes (they share a temp file name)
}

// Option configures Open.
type Option func(*config)

type config struct {
	embedder ragout.Embedder
	lexical  bool
}

// WithoutEmbeddings keeps recall on-device without requiring an Ollama service.
func WithoutEmbeddings() Option { return func(c *config) { c.lexical = true } }

// WithEmbedder replaces the default embedder (Ollama nomic-embed-text on localhost).
func WithEmbedder(e ragout.Embedder) Option { return func(c *config) { c.embedder = e } }

func Open(dbPath string, opts ...Option) (*Store, error) {
	if dbPath == "" {
		dbPath = "data/blooper.db"
	}
	cfg := config{embedder: embedder.NewOllamaEmbedder("nomic-embed-text")}
	for _, o := range opts {
		o(&cfg)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

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
	DROP TABLE IF EXISTS conversations_fts; -- replaced by ragout
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	chunks, err := chunker.NewCharacterChunker(800, 100, nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{
		db:       db,
		vec:      ragstore.NewVectorStore(),
		bm25:     ragstore.NewBM25Index(),
		vecPath:  dbPath + ".vec",
		bm25Path: dbPath + ".bm25",
	}
	options := []ragout.EngineOption{
		ragout.WithReader(reader.NewTextReader(0)),
		ragout.WithChunker(chunks),
		ragout.WithIndexStore(s.bm25),
	}
	if cfg.lexical {
		s.vecPath = dbPath + ".lexical.vec"
		s.bm25Path = dbPath + ".lexical.bm25"
	} else {
		options = append(options, ragout.WithEmbedder(cfg.embedder), ragout.WithVectorStore(s.vec))
	}
	s.rag, err = ragout.NewEngine(options...)
	if err != nil {
		db.Close()
		return nil, err
	}

	_, statErr := os.Stat(s.vecPath)
	if err := ragstore.LoadFile(s.vecPath, s.vec); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to load vector snapshot: %w", err)
	}
	if err := ragstore.LoadFile(s.bm25Path, s.bm25); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to load bm25 snapshot: %w", err)
	}
	if os.IsNotExist(statErr) {
		if err := s.reindex(context.Background()); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to index existing conversations (is Ollama running with nomic-embed-text pulled?): %w", err)
		}
	}
	return s, nil
}

// reindex feeds every stored conversation to ragout. Used when there is no snapshot yet,
// e.g. the first run after upgrading from the FTS5 index.
func (s *Store) reindex(ctx context.Context) error {
	rows, err := s.db.Query(`SELECT id, agent_name, user_query, summary, outcome, metadata FROM conversations ORDER BY id`)
	if err != nil {
		return err
	}
	var recs []ConversationRecord
	for rows.Next() {
		var r ConversationRecord
		var outcome, metadata sql.NullString
		if err := rows.Scan(&r.ID, &r.AgentName, &r.UserQuery, &r.Summary, &outcome, &metadata); err != nil {
			rows.Close()
			return err
		}
		r.Outcome, r.Metadata = outcome.String, metadata.String
		recs = append(recs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(recs) == 0 {
		return err
	}
	for _, r := range recs {
		if err := s.index(ctx, r); err != nil {
			return err
		}
	}
	return s.snapshot()
}

func (s *Store) index(ctx context.Context, r ConversationRecord) error {
	text := "Query: " + r.UserQuery + "\nSummary: " + r.Summary
	if r.Metadata != "" && r.Metadata != "{}" {
		text += "\nMetadata: " + r.Metadata
	}
	return s.rag.Ingest(ctx, fmt.Sprintf("conversation:%d", r.ID), strings.NewReader(text),
		map[string]any{"conversation_id": r.ID, "agent_name": r.AgentName})
}

func (s *Store) snapshot() error {
	s.snapMu.Lock()
	defer s.snapMu.Unlock()
	if err := ragstore.SaveFile(s.vecPath, s.vec); err != nil {
		return err
	}
	return ragstore.SaveFile(s.bm25Path, s.bm25)
}

// SaveConversation records the conversation and adds it to the search index. If indexing
// fails (e.g. Ollama is down) the record is kept but not searchable, and the error says so.
func (s *Store) SaveConversation(agentName, userQuery, summary, outcome, metadata string) error {
	res, err := s.db.Exec(`
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

	rec := ConversationRecord{ID: id, AgentName: agentName, UserQuery: userQuery, Summary: summary, Outcome: outcome, Metadata: metadata}
	if err := s.index(context.Background(), rec); err != nil {
		return fmt.Errorf("conversation %d saved but not indexed: %w", id, err)
	}
	return s.snapshot()
}

func (s *Store) Close() error {
	return s.db.Close()
}
