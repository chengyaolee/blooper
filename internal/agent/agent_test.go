package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/tools"
)

type bigLogTool struct{}

func (bigLogTool) Name() string            { return "read_log" }
func (bigLogTool) Description() string     { return "" }
func (bigLogTool) Schema() llm.FunctionDef { return llm.FunctionDef{Name: "read_log"} }
func (bigLogTool) RequiresApproval() bool  { return false }
func (bigLogTool) Execute(context.Context, json.RawMessage) (*tools.ToolResult, error) {
	return &tools.ToolResult{Output: strings.Repeat("INFO all good\n", 70000) + "FATAL disk on fire\n"}, nil
}

// scriptEngine plays back tool calls, then answers, recording every prompt size.
type scriptEngine struct {
	script []llm.ChatMessage
	sizes  []int
	last   []llm.ChatMessage
}

func (e *scriptEngine) Generate(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	n := 0
	for _, m := range req.Messages {
		n += len(m.Content)
	}
	e.sizes, e.last = append(e.sizes, n), req.Messages
	msg := llm.ChatMessage{Role: "assistant", Content: "done"}
	if len(e.script) > 0 {
		msg, e.script = e.script[0], e.script[1:]
	}
	return &llm.ChatResponse{Message: msg}, nil
}
func (e *scriptEngine) Stream(context.Context, *llm.ChatRequest, chan<- llm.StreamChunk) error {
	return nil
}
func (e *scriptEngine) KVStats() (*llm.KVMetrics, error)             { return nil, nil }
func (e *scriptEngine) ListModels(context.Context) ([]string, error) { return nil, nil }

func call(id, name, args string) llm.ChatMessage {
	return llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Function: llm.ToolCallFunction{Name: name, Arguments: args}}}}
}

func TestToolOutputNeverFloodsContext(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(bigLogTool{})
	eng := &scriptEngine{script: []llm.ChatMessage{
		call("1", "read_log", `{}`),
		call("2", "read_artifact", `{"id":"art-1","pattern":"FATAL"}`),
	}}
	a := NewAgent(&AgentConfig{}, eng, reg)

	if _, err := a.Run(context.Background(), "why did it crash?"); err != nil {
		t.Fatal(err)
	}
	for i, n := range eng.sizes {
		if n > 5000 {
			t.Errorf("prompt %d is %d chars; ~1MB log leaked into context", i, n)
		}
	}
	got := eng.last[len(eng.last)-1].Content
	if !strings.Contains(got, "70001: FATAL disk on fire") {
		t.Errorf("read_artifact result not in context: %q", got)
	}
}
