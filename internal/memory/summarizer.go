package memory

import (
	"context"
	"fmt"

	"github.com/chengyaolee/blooper/internal/llm"
)

type Summarizer struct {
	DefaultModel string
}

func NewSummarizer(defaultModel string) *Summarizer {
	if defaultModel == "" {
		defaultModel = "qwen3:8b"
	}
	return &Summarizer{DefaultModel: defaultModel}
}

func (s *Summarizer) Summarize(ctx context.Context, engine llm.InferenceEngine, model string, messages []llm.ChatMessage) (string, error) {
	if model == "" {
		model = s.DefaultModel
	}
	sysMsg := llm.ChatMessage{
		Role:    "system",
		Content: "Summarize the following conversation in 2-3 sentences. Focus on the main goal, actions taken, and the outcome.",
	}

	reqMsgs := []llm.ChatMessage{sysMsg}
	reqMsgs = append(reqMsgs, messages...)

	req := &llm.ChatRequest{
		Model:    model,
		Messages: reqMsgs,
	}

	resp, err := engine.Generate(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	return resp.Message.Content, nil
}
