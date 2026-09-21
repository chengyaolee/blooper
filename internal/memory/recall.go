package memory

import (
	"fmt"
	"time"
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

func (s *Store) Recall(query string, limit int) ([]ConversationRecord, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.agent_name, c.user_query, c.summary, c.outcome, c.created_at, c.metadata
		FROM conversations_fts f
		JOIN conversations c ON f.rowid = c.id
		WHERE conversations_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to execute recall query: %w", err)
	}
	defer rows.Close()

	var records []ConversationRecord
	for rows.Next() {
		var rec ConversationRecord
		var createdAt string
		if err := rows.Scan(&rec.ID, &rec.AgentName, &rec.UserQuery, &rec.Summary, &rec.Outcome, &createdAt, &rec.Metadata); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		t, err := time.Parse("2006-01-02 15:04:05", createdAt)
		if err == nil {
			rec.CreatedAt = t
		}
		records = append(records, rec)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return records, nil
}
