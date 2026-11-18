package ui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/core"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/tools"
)

// This test uses a real model through the same SSE route the browser uses.
// Explicit opt-in keeps the normal test suite offline and free of model costs.
func TestLiveChatDelegation(t *testing.T) {
	if os.Getenv("BLOOPER_LIVE_OLLAMA") != "1" && os.Getenv("BLOOPER_LIVE_OPENAI_FLEET") != "1" {
		t.Skip("set BLOOPER_LIVE_OLLAMA=1 to run a real chat delegation chain")
	}
	ollama := llm.NewOllamaClient(os.Getenv("OLLAMA_BASE_URL"))
	ollama.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	var engine llm.InferenceEngine = ollama
	if os.Getenv("BLOOPER_LIVE_OPENAI_FLEET") == "1" {
		if os.Getenv("OPENAI_API_KEY") == "" {
			t.Fatal("OPENAI_API_KEY required")
		}
		provider := &llm.Provider{}
		provider.Set("openai", llm.OpenAIModel, llm.NewOpenAIClient("", os.Getenv("OPENAI_API_KEY")))
		engine = provider
	}
	agents := agent.NewAgentRegistry()
	if err := agents.LoadDir("../../agents"); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	var unexpected atomic.Int32
	for _, name := range []string{"web_search", "browser", "file_write", "recall", "create_agent_config"} {
		reg.Register(&liveObservedTool{name: name, unexpected: &unexpected})
	}
	delegated := &liveObservedTool{Tool: tools.NewDelegateTool("", core.NewDelegator(agents, engine, reg))}
	reg.Register(delegated)
	srv := httptest.NewServer(NewServer(ServerConfig{DefaultAgent: "web_researcher"}, engine, agents, reg, core.NewApprovalQueue(), nil).Routes())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/chat", strings.NewReader(`{"message":"Use the delegate tool to ask email_drafter to draft a short email from Alex to Sam. Supplied facts: our Blooper demo is Thursday at 10 AM, and Sam should confirm attendance. Pass all these facts to email_drafter, then return the email it gives you. All research is supplied here: do not browse, search, recall, or save files."}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if delegated.calls.Load() == 0 || !delegated.success.Load() {
		t.Fatalf("no successful delegation:\n%s", data)
	}
	if unexpected.Load() != 0 {
		t.Fatalf("model called unexpected tools:\n%s", data)
	}
	if !strings.Contains(string(data), "Thursday") || strings.Contains(string(data), `"error"`) {
		t.Fatalf("draft missing or failed:\n%s", data)
	}
	t.Logf("Real web_researcher -> email_drafter chain via /api/chat; %d successful tool call(s). SSE response:\n%s", delegated.calls.Load(), data)
}

type liveObservedTool struct {
	tools.Tool
	name       string
	unexpected *atomic.Int32
	calls      atomic.Int32
	success    atomic.Bool
}

func (t *liveObservedTool) Name() string {
	if t.Tool != nil {
		return t.Tool.Name()
	}
	return t.name
}
func (t *liveObservedTool) Description() string {
	if t.Tool != nil {
		return t.Tool.Description()
	}
	return "Not needed for this supplied-facts task"
}
func (t *liveObservedTool) RequiresApproval() bool { return false }
func (t *liveObservedTool) Schema() llm.FunctionDef {
	if t.Tool != nil {
		return t.Tool.Schema()
	}
	return llm.FunctionDef{Name: t.name, Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}}
}
func (t *liveObservedTool) Execute(ctx context.Context, args json.RawMessage) (*tools.ToolResult, error) {
	t.calls.Add(1)
	if t.Tool == nil {
		t.unexpected.Add(1)
		return &tools.ToolResult{Error: "Use only supplied facts; do not call this tool"}, nil
	}
	result, err := t.Tool.Execute(ctx, args)
	if err == nil && result.Error == "" {
		t.success.Store(true)
	}
	return result, err
}
