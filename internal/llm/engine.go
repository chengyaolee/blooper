package llm

import (
    "context"
)

// ChatMessage represents a message in a conversation.
type ChatMessage struct {
    Role       string      `json:"role"` // "system", "user", "assistant", "tool"
    Content    string      `json:"content"`
    ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
    ToolCallID string      `json:"tool_call_id,omitempty"`
}

// ToolCall represents a function call requested by the model.
type ToolCall struct {
    ID       string          `json:"id"`
    Type     string          `json:"type"` // "function"
    Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the function name and JSON-encoded arguments.
type ToolCallFunction struct {
    Name      string `json:"name"`
    Arguments string `json:"arguments"` // raw JSON string
}

// ToolDef describes a tool available to the model.
type ToolDef struct {
    Type     string       `json:"type"` // "function"
    Function FunctionDef  `json:"function"`
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
    Message   ChatMessage `json:"message"`
    Done      bool        `json:"done"`
    Model     string      `json:"model"`
    Usage     Usage       `json:"usage"`
}

// Usage holds token accounting reported by the engine (zero when unknown).
type Usage struct {
    PromptTokens     int `json:"prompt_tokens"`
    CompletionTokens int `json:"completion_tokens"`
}

// StreamChunk is a single token/chunk in a streaming response.
type StreamChunk struct {
    Content   string     `json:"content"`
    ToolCalls []ToolCall `json:"tool_calls,omitempty"`
    Done      bool       `json:"done"`
    Usage     Usage      `json:"usage"` // set on the final chunk
    Error     error      `json:"-"`
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
