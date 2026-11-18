package llm

import (
	"context"
)

// ChatMessage represents a message in a conversation.
type ChatMessage struct {
	Role       string     `json:"role"` // "system", "user", "assistant", "tool"
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Images     []Image    `json:"images,omitempty"` // provider adapters encode these as image inputs
}

// Image is an in-memory screenshot, never an artifact or text tool result.
type Image struct {
	Data   string `json:"data"` // base64 JPEG
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ToolCall represents a function call requested by the model.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the function name and JSON-encoded arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string
}

// ToolDef describes a tool available to the model.
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef defines a callable function.
type FunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"` // JSON Schema object
}

// ChatRequest is a request to the inference engine.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Tools       []ToolDef     `json:"tools,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

// ChatResponse is a response from the inference engine.
type ChatResponse struct {
	Message ChatMessage `json:"message"`
	Done    bool        `json:"done"`
	Model   string      `json:"model"`
}

// StreamChunk is a single token/chunk in a streaming response.
type StreamChunk struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Done      bool       `json:"done"`
	Error     error      `json:"-"`
	// ApprovalID is set when the agent has already queued this chunk's tool call
	// for human approval, so the UI shows that item instead of queuing another.
	ApprovalID string `json:"-"`
}

// KVMetrics holds KV cache statistics (for monitoring/debugging).
type KVMetrics struct {
	ActiveSlots     int   `json:"active_slots"`
	TotalSlots      int   `json:"total_slots"`
	UsedMemoryBytes int64 `json:"used_memory_bytes"`
}

// InferenceEngine abstracts the LLM backend.
type InferenceEngine interface {
	Generate(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	Stream(ctx context.Context, req *ChatRequest, out chan<- StreamChunk) error
	KVStats() (*KVMetrics, error)
	ListModels(ctx context.Context) ([]string, error)
}
