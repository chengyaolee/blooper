package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveOpenAI(t *testing.T) {
	if os.Getenv("BLOOPER_LIVE_HOSTED") != "1" {
		t.Skip("set BLOOPER_LIVE_HOSTED=1 and OPENAI_API_KEY")
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Fatal("OPENAI_API_KEY is required")
	}
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = OpenAIModel
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := NewOpenAIClient(os.Getenv("OPENAI_BASE_URL"), key)
	req := &ChatRequest{Model: model, MaxTokens: 1024, Messages: []ChatMessage{{Role: "user", Content: "Call echo once with text blooper-provider-check. After getting the result, reply with that text only."}}, Tools: []ToolDef{{Type: "function", Function: FunctionDef{Name: "echo", Description: "Echo the supplied text", Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}}, "required": []string{"text"}}}}}}
	resp, err := client.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Function.Name != "echo" {
		t.Fatalf("expected echo tool call: %+v", resp.Message)
	}
	req.Messages = append(req.Messages, resp.Message, ChatMessage{Role: "tool", ToolCallID: resp.Message.ToolCalls[0].ID, Content: "blooper-provider-check"})
	chunks := make(chan StreamChunk, 64)
	if err := client.Stream(ctx, req, chunks); err != nil {
		t.Fatal(err)
	}
	close(chunks)
	var text strings.Builder
	for chunk := range chunks {
		text.WriteString(chunk.Content)
	}
	if !strings.Contains(text.String(), "blooper-provider-check") {
		t.Fatalf("streamed result missing: %q", text.String())
	}
	t.Logf("OpenAI %s: real tool call, tool result and streaming passed", model)
}
