package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
)

// RecallTool allows agents to recall past interactions and knowledge from persistent memory (hybrid BM25 + vector search via ragout).
type RecallTool struct {
	store *memory.Store
}

func NewRecallTool(store *memory.Store) *RecallTool {
	return &RecallTool{store: store}
}

func (t *RecallTool) Name() string {
	return "recall"
}

func (t *RecallTool) Description() string {
	return "Searches persistent memory for past conversations, decisions, contacts, or research findings."
}

func (t *RecallTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Keywords or search phrase to recall from past memory (e.g. 'Acme Corp contact', 'meeting rules')",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of past records to return (default 5)",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *RecallTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	if t.store == nil {
		return &ToolResult{Output: "Memory store is currently uninitialized. No past records available."}, nil
	}

	var params struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &ToolResult{Error: "invalid arguments"}, nil
	}

	if params.Limit <= 0 {
		params.Limit = 5
	}
	if params.Limit > 20 {
		params.Limit = 20
	}

	records, err := t.store.Recall(params.Query, params.Limit)
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("failed to search memory: %v", err)}, nil
	}

	if len(records) == 0 {
		return &ToolResult{Output: fmt.Sprintf("No past memories found matching query %q.", params.Query)}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d relevant past memory record(s):\n", len(records)))
	for i, r := range records {
		sb.WriteString(fmt.Sprintf("\n[%d] Agent: %s | Date: %s\nQuery: %s\nSummary: %s\n",
			i+1, r.AgentName, r.CreatedAt.Format("2006-01-02 15:04"), r.UserQuery, r.Summary))
		if r.Metadata != "" && r.Metadata != "{}" {
			sb.WriteString(fmt.Sprintf("Metadata: %s\n", r.Metadata))
		}
	}

	return &ToolResult{Output: sb.String()}, nil
}

func (t *RecallTool) RequiresApproval() bool {
	return false
}
