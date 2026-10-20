package core

import (
	"context"
	"fmt"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/tools"
	"github.com/chengyaolee/blooper/internal/trace"
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
	store         *memory.Store // optional: records delegated runs as children of the caller's run
}

func NewDelegator(registry *agent.AgentRegistry, engine llm.InferenceEngine, toolReg *tools.ToolRegistry, store *memory.Store) *Delegator {
	return &Delegator{
		agentRegistry: registry,
		engine:        engine,
		toolRegistry:  toolReg,
		store:         store,
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

// DelegateRequest executes a structured delegation request.
func (d *Delegator) DelegateRequest(ctx context.Context, req DelegationRequest) (*DelegationResult, error) {
	cfg, ok := d.agentRegistry.Get(req.ToAgent)
	if !ok {
		return nil, fmt.Errorf("target agent %q not found", req.ToAgent)
	}

	toolReg := d.toolRegistry
	if toolReg == nil {
		toolReg = tools.NewRegistry()
	}
	a := agent.NewAgent(cfg, d.engine, toolReg)

	ctx, run := trace.Start(ctx, d.store, trace.Info{Agent: cfg.Name, Role: "delegate", Model: cfg.Model.Preferred, Input: req.Task, Config: cfg})
	result, err := a.Run(ctx, req.Task)
	run.End(result, err)
	if err != nil {
		return &DelegationResult{Error: err.Error()}, nil
	}

	return &DelegationResult{Output: result}, nil
}
