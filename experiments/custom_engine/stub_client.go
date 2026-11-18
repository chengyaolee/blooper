package customengine

// This is a STUB example showing how to implement a custom InferenceEngine
// that connects to your own inference server instead of Ollama.
//
// To use this:
// 1. Build your own inference server (C++, Python, Rust, etc.)
// 2. Implement the InferenceEngine interface methods below
// 3. Register it in cmd/blooper/main.go

import (
	"context"
	"fmt"
	"github.com/chengyaolee/blooper/internal/llm"
)

type CustomEngine struct {
	serverURL string
}

func NewCustomEngine(url string) *CustomEngine {
	return &CustomEngine{serverURL: url}
}

func (e *CustomEngine) Generate(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	// TODO: Implement your custom inference call here
	return nil, fmt.Errorf("custom engine not implemented — this is your playground!")
}

func (e *CustomEngine) Stream(ctx context.Context, req *llm.ChatRequest, out chan<- llm.StreamChunk) error {
	// TODO: Implement streaming from your custom server
	return fmt.Errorf("custom engine streaming not implemented")
}

func (e *CustomEngine) KVStats() (*llm.KVMetrics, error) {
	// TODO: Query your custom server for KV cache metrics
	return &llm.KVMetrics{}, nil
}

func (e *CustomEngine) ListModels(ctx context.Context) ([]string, error) {
	// TODO: Return available models from your custom server
	return []string{"custom-model-v1"}, nil
}
