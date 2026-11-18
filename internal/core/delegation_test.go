package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/tools"
)

type answerEngine struct{}

func (answerEngine) Generate(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: "ok"}}, nil
}
func (answerEngine) Stream(context.Context, *llm.ChatRequest, chan<- llm.StreamChunk) error {
	return nil
}
func (answerEngine) KVStats() (*llm.KVMetrics, error)             { return nil, nil }
func (answerEngine) ListModels(context.Context) ([]string, error) { return nil, nil }

func newDelegator() *Delegator {
	reg := agent.NewAgentRegistry()
	add := func(name string, d agent.DelegationConfig) {
		reg.Register(&agent.AgentConfig{
			Name:       name,
			Persona:    agent.PersonaConfig{Role: "r", SystemPrompt: "p"},
			Model:      agent.ModelConfig{Preferred: "m"},
			Delegation: d,
		})
	}
	add("lead", agent.DelegationConfig{CanDelegateTo: []string{"writer", "picky"}, MaxDelegationDepth: 1})
	add("other", agent.DelegationConfig{CanDelegateTo: []string{"picky"}, MaxDelegationDepth: 1})
	add("writer", agent.DelegationConfig{})
	add("picky", agent.DelegationConfig{CanBeDelegatedBy: []string{"lead"}})
	return NewDelegator(reg, answerEngine{}, nil)
}

func TestDelegationRules(t *testing.T) {
	d := newDelegator()
	cases := []struct {
		name     string
		from, to string
		depth    int
		wantErr  string // empty means allowed
	}{
		{"listed target", "lead", "writer", 0, ""},
		{"target not listed", "lead", "other", 0, "may not delegate"},
		{"caller with no delegation config", "writer", "lead", 0, "may not delegate"},
		{"target accepts this caller", "lead", "picky", 0, ""},
		{"target refuses this caller", "other", "picky", 0, "does not accept work"},
		{"chain already at the limit", "lead", "writer", 1, "depth limit"},
		{"itself", "lead", "lead", 0, "itself"},
		{"unknown caller", "ghost", "writer", 0, "not registered"},
		{"unknown target", "lead", "ghost", 0, "not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := tools.WithDelegationDepth(context.Background(), c.depth)
			out, err := d.Delegate(ctx, c.from, c.to, "do it")
			switch {
			case c.wantErr == "" && (err != nil || out != "ok"):
				t.Fatalf("got %q, %v; want the delegate's answer", out, err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("got %q, %v; want an error containing %q", out, err, c.wantErr)
			}
		})
	}
}

// The delegate tool is shared by every agent. The executing agent has to reach
// the rules through the context, not through the tool's own fields.
func TestDelegateToolUsesExecutingAgent(t *testing.T) {
	tool := tools.NewDelegateTool("", newDelegator())
	args := json.RawMessage(`{"agent":"writer","task":"do it"}`)

	allowed, _ := tool.Execute(tools.WithCaller(context.Background(), "lead"), args)
	if allowed.Error != "" || !strings.Contains(allowed.Output, "ok") {
		t.Errorf("lead was refused: %+v", allowed)
	}
	denied, _ := tool.Execute(tools.WithCaller(context.Background(), "other"), args)
	if !strings.Contains(denied.Error, "may not delegate") {
		t.Errorf("other should not be able to reach writer: %+v", denied)
	}
}
