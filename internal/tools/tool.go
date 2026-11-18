package tools

import (
	"context"
	"encoding/json"
	"github.com/chengyaolee/blooper/internal/llm"
)

type ToolResult struct {
	Output string     `json:"output"`
	Error  string     `json:"error,omitempty"`
	Image  *llm.Image `json:"-"`
}

type Tool interface {
	Name() string
	Description() string
	Schema() llm.FunctionDef
	Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error)
	RequiresApproval() bool
}
