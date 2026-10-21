package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chengyaolee/ragout"
)

type ConversationRecord struct {
	ID        int64
	AgentName string
	UserQuery string
	Summary   string
	Outcome   string
	CreatedAt time.Time
	Metadata  string
}

// Recall returns up to limit past conversations, best match first.
func (s *Store) Recall(query string, limit int) ([]ConversationRecord, error) {
	ans, err := s.rag.Retrieve(context.Background(), query, ragout.WithQueryTopNRerank(limit))
	if errors.Is(err, ragout.ErrNoResults) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to execute recall query: %w", err)
	}

	// A long conversation can match through several chunks; keep the first (best) hit.
	seen := map[int64]bool{}
	var records []ConversationRecord
	for _, hit := range ans.Retrieved {
		id, _ := hit.Chunk.Metadata["conversation_id"].(int64)
		if seen[id] {
			continue
		}
		seen[id] = true

		var rec ConversationRecord
		var outcome, metadata sql.NullString
		var createdAt string
		err := s.db.QueryRow(`
			SELECT id, agent_name, user_query, summary, outcome, created_at, metadata
			FROM conversations WHERE id = ?
		`, id).Scan(&rec.ID, &rec.AgentName, &rec.UserQuery, &rec.Summary, &outcome, &createdAt, &metadata)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		rec.Outcome, rec.Metadata = outcome.String, metadata.String
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			rec.CreatedAt = t
		}
		records = append(records, rec)
	}
	return records, nil
}
