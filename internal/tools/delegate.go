package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chengyaolee/blooper/internal/llm"
)

// SubtaskDelegator is an interface to delegate subtasks to other agents.
type SubtaskDelegator interface {
	Delegate(ctx context.Context, fromAgent, toAgent, task string) (string, error)
}

// DelegateTool allows an agent to delegate a subtask to a specialized peer agent.
type DelegateTool struct {
	CurrentAgent string
	Delegator    SubtaskDelegator
}

func NewDelegateTool(currentAgent string, delegator SubtaskDelegator) *DelegateTool {
	return &DelegateTool{
		CurrentAgent: currentAgent,
		Delegator:    delegator,
	}
}

func (t *DelegateTool) Name() string {
	return "delegate"
}

func (t *DelegateTool) Description() string {
	return "Delegates a specific subtask to another specialized agent in the fleet (e.g. 'email_drafter', 'web_researcher') and waits for the result."
}

func (t *DelegateTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent": map[string]interface{}{
					"type":        "string",
					"description": "Name of the target agent to delegate to (e.g. 'email_drafter')",
				},
				"task": map[string]interface{}{
					"type":        "string",
					"description": "Clear, detailed prompt and context for the subtask",
				},
			},
			"required": []string{"agent", "task"},
		},
	}
}

func (t *DelegateTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	if t.Delegator == nil {
		return &ToolResult{Error: "delegation engine is uninitialized"}, nil
	}

	var params struct {
		Agent string `json:"agent"`
		Task  string `json:"task"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return &ToolResult{Error: "invalid arguments"}, nil
	}

	// The tool is shared by all agents, so the executing agent comes from the context.
	caller := Caller(ctx)
	if caller == "" {
		caller = t.CurrentAgent
	}

	// Prevent self-delegation loop
	if params.Agent == caller {
		return &ToolResult{Error: fmt.Sprintf("agent %q cannot delegate to itself", caller)}, nil
	}

	output, err := t.Delegator.Delegate(ctx, caller, params.Agent, params.Task)
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("delegation to %s failed: %v", params.Agent, err)}, nil
	}

	return &ToolResult{
		Output: fmt.Sprintf("Subtask delegated to [%s] completed with result:\n\n%s", params.Agent, output),
	}, nil
}

func (t *DelegateTool) RequiresApproval() bool {
	return false
}
