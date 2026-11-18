package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/core"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/tools"
)

type chainEngine struct{}

func (chainEngine) Generate(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	msg := llm.ChatMessage{Role: "assistant", Content: "done"}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == "tool" {
		msg.Content = last.Content
	} else {
		name, args := "delegate", `{"agent":"writer","task":"draft"}`
		if strings.HasPrefix(req.Messages[0].Content, "writer\n") {
			name, args = "write", `{}`
		}
		msg.ToolCalls = []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: args}}}
	}
	return &llm.ChatResponse{Message: msg}, nil
}
func (e chainEngine) Stream(ctx context.Context, req *llm.ChatRequest, out chan<- llm.StreamChunk) error {
	resp, err := e.Generate(ctx, req)
	if err != nil {
		return err
	}
	out <- llm.StreamChunk{Content: resp.Message.Content, Done: true}
	return nil
}
func (chainEngine) KVStats() (*llm.KVMetrics, error)             { return &llm.KVMetrics{}, nil }
func (chainEngine) ListModels(context.Context) ([]string, error) { return []string{"m"}, nil }

type countedTool struct {
	calls     atomic.Int32
	caller    atomic.Value
	intrinsic bool
}

func (*countedTool) Name() string             { return "write" }
func (*countedTool) Description() string      { return "write" }
func (t *countedTool) RequiresApproval() bool { return t.intrinsic }
func (*countedTool) Schema() llm.FunctionDef  { return llm.FunctionDef{Name: "write"} }
func (t *countedTool) Execute(ctx context.Context, _ json.RawMessage) (*tools.ToolResult, error) {
	t.calls.Add(1)
	t.caller.Store(tools.Caller(ctx))
	return &tools.ToolResult{Output: "draft saved"}, nil
}

// Exercises the real chat SSE endpoint, nested agent loop and approval endpoint.
func TestChatDelegationApproval(t *testing.T) {
	for _, decision := range []string{"approve", "reject", "cancel", "intrinsic"} {
		t.Run(decision, func(t *testing.T) {
			agents := agent.NewAgentRegistry()
			agents.Register(&agent.AgentConfig{Name: "researcher", Persona: agent.PersonaConfig{SystemPrompt: "researcher"}, Model: agent.ModelConfig{Preferred: "m"}, Tools: []string{"delegate"}, Delegation: agent.DelegationConfig{CanDelegateTo: []string{"writer"}, MaxDelegationDepth: 1}})
			writer := &agent.AgentConfig{Name: "writer", Persona: agent.PersonaConfig{SystemPrompt: "writer"}, Model: agent.ModelConfig{Preferred: "m"}, Tools: []string{"write"}}
			if decision != "intrinsic" {
				writer.Approval.RequireApprovalFor = []string{"write"}
			}
			agents.Register(writer)
			reg := tools.NewRegistry()
			write := &countedTool{intrinsic: decision == "intrinsic"}
			reg.Register(write)
			reg.Register(tools.NewDelegateTool("", core.NewDelegator(agents, chainEngine{}, reg)))
			queue := core.NewApprovalQueue()
			srv := httptest.NewServer(NewServer(ServerConfig{DefaultAgent: "researcher"}, chainEngine{}, agents, reg, queue, nil).Routes())
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/chat", strings.NewReader(`{"message":"draft"}`))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			scanner := bufio.NewScanner(resp.Body)
			var id string
			for scanner.Scan() {
				line := strings.TrimPrefix(scanner.Text(), "data: ")
				var event struct {
					Approval struct {
						ID string `json:"id"`
					} `json:"approval"`
				}
				_ = json.Unmarshal([]byte(line), &event)
				if event.Approval.ID != "" {
					id = event.Approval.ID
					break
				}
			}
			if id == "" {
				t.Fatalf("no approval card: %v", scanner.Err())
			}
			if len(queue.ListPending()) != 1 || write.calls.Load() != 0 {
				t.Fatal("action ran before approval or duplicated the request")
			}
			if queue.Get(id).AgentName != "writer" {
				t.Fatal("delegated approval lost its caller")
			}
			if decision == "cancel" {
				cancel()
				resp.Body.Close()
				deadline := time.Now().Add(time.Second)
				for queue.Get(id).Status == "pending" && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if queue.Get(id).Status != "cancelled" {
					t.Fatal("approval survived request cancellation")
				}
			} else {
				action := decision
				if action == "intrinsic" {
					action = "approve"
				}
				resolve, err := http.Post(srv.URL+"/api/approvals/"+id+"/"+action, "application/json", nil)
				if err != nil {
					t.Fatal(err)
				}
				resolve.Body.Close()
				if resolve.StatusCode != 200 {
					t.Fatalf("decision status %d", resolve.StatusCode)
				}
				var tail strings.Builder
				for scanner.Scan() {
					tail.WriteString(scanner.Text())
				}
				if err := scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if decision != "reject" && !strings.Contains(tail.String(), "draft saved") {
					t.Fatal("result did not return to chat")
				}
			}
			want := int32(0)
			if decision == "approve" || decision == "intrinsic" {
				want = 1
			}
			if write.calls.Load() != want {
				t.Fatalf("executions %d, want %d", write.calls.Load(), want)
			}
			if want == 1 && write.caller.Load() != "writer" {
				t.Fatal("approved execution lost its caller")
			}
			again, err := http.Post(srv.URL+"/api/approvals/"+id+"/approve", "application/json", nil)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, again.Body)
			again.Body.Close()
			if again.StatusCode != 409 {
				t.Fatalf("reused approval: %d", again.StatusCode)
			}
		})
	}
}
