package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/tools"
)

type echoEngine struct{}

func (echoEngine) Generate(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	if req.Model == "broken" {
		return nil, errors.New("model offline")
	}
	return &llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: "hi"}, Usage: llm.Usage{PromptTokens: 7, CompletionTokens: 2}}, nil
}

func (echoEngine) Stream(ctx context.Context, req *llm.ChatRequest, out chan<- llm.StreamChunk) error {
	defer close(out)
	out <- llm.StreamChunk{Content: "he"}
	out <- llm.StreamChunk{Content: "llo", Done: true, Usage: llm.Usage{PromptTokens: 3, CompletionTokens: 2}}
	return nil
}
func (echoEngine) KVStats() (*llm.KVMetrics, error)                 { return &llm.KVMetrics{}, nil }
func (echoEngine) ListModels(ctx context.Context) ([]string, error) { return nil, nil }

type upperTool struct{}

func (upperTool) Name() string            { return "upper" }
func (upperTool) Description() string     { return "uppercases" }
func (upperTool) Schema() llm.FunctionDef { return llm.FunctionDef{Name: "upper"} }
func (upperTool) RequiresApproval() bool  { return false }
func (upperTool) Execute(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	return &tools.ToolResult{Output: "OK"}, nil
}

func TestTraceRecordsNestedRunsAndExports(t *testing.T) {
	store, err := memory.Open(filepath.Join(t.TempDir(), "trace.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	engine := Engine(echoEngine{})
	tool := Tool(Tool(upperTool{})) // idempotent wrap
	req := &llm.ChatRequest{Model: "m", Messages: []llm.ChatMessage{{Role: "user", Content: "x"}},
		Tools: []llm.ToolDef{{Type: "function", Function: upperTool{}.Schema()}}}

	// Untraced context: passes through, records nothing.
	if _, err := engine.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	ctx, root := Start(context.Background(), store, Info{Agent: "chat_agent", Role: "chat", Input: "hello", Config: map[string]string{"model": "m"}})
	_, _ = engine.Generate(ctx, req)
	_, _ = engine.Generate(ctx, &llm.ChatRequest{Model: "broken"})
	_, _ = tool.Execute(ctx, json.RawMessage(`{"s":"a"}`))

	child, sub := Start(ctx, store, Info{Agent: "helper", Role: "delegate"})
	out := make(chan llm.StreamChunk, 4)
	go func() { _ = engine.Stream(child, req, out) }()
	var streamed string
	for c := range out {
		streamed += c.Content
	}
	sub.End(streamed, nil)
	root.End("bye", nil)

	if streamed != "hello" {
		t.Fatalf("stream passthrough: %q", streamed)
	}

	var buf bytes.Buffer
	n, err := store.ExportJSONL(&buf)
	if err != nil || n != 1 {
		t.Fatalf("export: n=%d err=%v", n, err)
	}
	var ep struct {
		Runs []struct {
			R          string `json:"role"`
			P          string `json:"parent_id"`
			Status     string `json:"status"`
			ConfigHash string `json:"config_hash"`
			Steps      []struct {
				Kind         string          `json:"kind"`
				Name         string          `json:"name"`
				Error        string          `json:"error"`
				PromptTokens int             `json:"prompt_tokens"`
				Input        json.RawMessage `json:"input"`
			} `json:"steps"`
		} `json:"runs"`
		Blobs map[string]json.RawMessage `json:"blobs"`
	}
	if err := json.Unmarshal(buf.Bytes(), &ep); err != nil {
		t.Fatalf("decode export: %v\n%s", err, buf.String())
	}
	if len(ep.Runs) != 2 || ep.Runs[0].R != "chat" || ep.Runs[1].R != "delegate" || ep.Runs[1].P == "" {
		t.Fatalf("want chat run with nested delegate run, got %+v", ep.Runs)
	}
	steps := ep.Runs[0].Steps
	if len(steps) != 3 || steps[0].Kind != "llm" || steps[0].PromptTokens != 7 || steps[1].Error != "model offline" || steps[2].Kind != "tool" {
		t.Fatalf("unexpected root steps: %+v", steps)
	}
	if s := ep.Runs[1].Steps; len(s) != 1 || s[0].PromptTokens != 3 {
		t.Fatalf("stream step not recorded: %+v", s)
	}
	var in struct {
		ToolsHash string `json:"tools_hash"`
	}
	_ = json.Unmarshal(steps[0].Input, &in)
	if _, ok := ep.Blobs[in.ToolsHash]; !ok || ep.Blobs[ep.Runs[0].ConfigHash] == nil {
		t.Fatalf("tool schemas / agent config blobs missing from export: %v", ep.Blobs)
	}
}
