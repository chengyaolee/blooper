package core

import (
	"context"
	"fmt"
	"slices"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/tools"
)

type DelegationRequest struct {
	FromAgent string
	ToAgent   string
	Task      string
}

type DelegationResult struct {
	Output string
	Error  string
}

// Delegator coordinates subtask execution between specialized agents.
type Delegator struct {
	agentRegistry *agent.AgentRegistry
	engine        llm.InferenceEngine
	toolRegistry  *tools.ToolRegistry
}

func NewDelegator(registry *agent.AgentRegistry, engine llm.InferenceEngine, toolReg *tools.ToolRegistry) *Delegator {
	return &Delegator{
		agentRegistry: registry,
		engine:        engine,
		toolRegistry:  toolReg,
	}
}

// Delegate satisfies tools.SubtaskDelegator for the delegate tool.
func (d *Delegator) Delegate(ctx context.Context, fromAgent, toAgent, task string) (string, error) {
	res, err := d.DelegateRequest(ctx, DelegationRequest{
		FromAgent: fromAgent,
		ToAgent:   toAgent,
		Task:      task,
	})
	if err != nil {
		return "", err
	}
	if res.Error != "" {
		return "", fmt.Errorf("%s", res.Error)
	}
	return res.Output, nil
}

// authorize applies the delegation rules from the agents' YAML:
//   - the caller's can_delegate_to must list the target (empty means it may not delegate);
//   - if the target sets can_be_delegated_by, it must list the caller (empty means anyone
//     the caller is allowed to delegate to);
//   - the chain may not grow past the caller's max_delegation_depth (0 means none).
//
// A top-level agent is at depth 0, and each hop adds one.
func (d *Delegator) authorize(ctx context.Context, from string, to *agent.AgentConfig) error {
	caller, ok := d.agentRegistry.Get(from)
	if !ok {
		return fmt.Errorf("calling agent %q is not registered", from)
	}
	if from == to.Name {
		return fmt.Errorf("agent %q cannot delegate to itself", from)
	}
	if !slices.Contains(caller.Delegation.CanDelegateTo, to.Name) {
		return fmt.Errorf("agent %q may not delegate to %q (see can_delegate_to)", from, to.Name)
	}
	if by := to.Delegation.CanBeDelegatedBy; len(by) > 0 && !slices.Contains(by, from) {
		return fmt.Errorf("agent %q does not accept work from %q (see can_be_delegated_by)", to.Name, from)
	}
	if depth := tools.DelegationDepth(ctx); depth+1 > caller.Delegation.MaxDelegationDepth {
		return fmt.Errorf("delegation depth limit reached: %q allows %d hop(s) and this chain is already %d deep", from, caller.Delegation.MaxDelegationDepth, depth)
	}
	return nil
}

// DelegateRequest executes a structured delegation request.
func (d *Delegator) DelegateRequest(ctx context.Context, req DelegationRequest) (*DelegationResult, error) {
	cfg, ok := d.agentRegistry.Get(req.ToAgent)
	if !ok {
		return nil, fmt.Errorf("target agent %q not found", req.ToAgent)
	}
	if err := d.authorize(ctx, req.FromAgent, cfg); err != nil {
		return nil, err
	}
	ctx = tools.WithDelegationDepth(ctx, tools.DelegationDepth(ctx)+1)

	toolReg := d.toolRegistry
	if toolReg == nil {
		toolReg = tools.NewRegistry()
	}
	a := agent.NewAgent(cfg, d.engine, toolReg)

	result, err := a.Run(ctx, req.Task)
	if err != nil {
		return &DelegationResult{Error: err.Error()}, nil
	}

	return &DelegationResult{Output: result}, nil
}
