package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/tools"
)

const maxIterations = 20

// ApprovalSubmitter is an interface for queueing actions that require human sign-off.
type ApprovalSubmitter interface {
	Submit(agentName, toolName, arguments string) string
}

// Agent represents an executing agent instance.
type Agent struct {
	Config          *AgentConfig
	Engine          llm.InferenceEngine
	ToolRegistry    *tools.ToolRegistry
	ApprovalHandler ApprovalSubmitter
	// Memory decides what goes into each prompt: user rules (MEMORY.md), task
	// state, long-term summaries, artifact pointers, and a verbatim scratchpad.
	Memory *memory.Orchestrator
}

// NewAgent creates a new Agent instance.
func NewAgent(cfg *AgentConfig, engine llm.InferenceEngine, registry *tools.ToolRegistry) *Agent {
	return &Agent{
		Config:       cfg,
		Engine:       engine,
		ToolRegistry: registry,
		Memory:       memory.NewOrchestrator(engine, cfg.Model.Preferred),
	}
}

// SetApprovalHandler configures the approval handler for the agent.
func (a *Agent) SetApprovalHandler(handler ApprovalSubmitter) {
	a.ApprovalHandler = handler
}

// sendChunk safely sends a stream chunk respecting context cancellation to prevent goroutine leaks.
func sendChunk(ctx context.Context, out chan<- llm.StreamChunk, chunk llm.StreamChunk) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- chunk:
		return true
	}
}

// nextRequest compacts memory and builds the layered prompt for the next model call.
func (a *Agent) nextRequest(ctx context.Context, toolDefs []llm.ToolDef) *llm.ChatRequest {
	a.Memory.Compact(ctx)
	return &llm.ChatRequest{
		Model:       a.Config.Model.Preferred,
		Messages:    a.Memory.Messages(a.Config.Persona.SystemPrompt),
		Tools:       toolDefs,
		Temperature: a.Config.Model.Temperature,
	}
}

func (a *Agent) toolDefs() []llm.ToolDef {
	return append(a.ToolRegistry.ToToolDefs(), a.Memory.Tools()...)
}

// runTools executes the tool calls in msg and records them as one scratchpad
// step. Tool output goes through the memory orchestrator, so large results are
// stored as artifacts and only a digest reaches the context. If out is
// non-nil, progress is streamed to the UI.
func (a *Agent) runTools(ctx context.Context, msg llm.ChatMessage, out chan<- llm.StreamChunk) {
	step := []llm.ChatMessage{msg}
	for _, tc := range msg.ToolCalls {
		reply := func(content string) {
			step = append(step, llm.ChatMessage{Role: "tool", ToolCallID: tc.ID, Content: content})
		}

		if result, ok := a.Memory.HandleTool(tc.Function.Name, tc.Function.Arguments); ok {
			reply(result)
			continue
		}

		tool, ok := a.ToolRegistry.Get(tc.Function.Name)
		if !ok {
			reply(fmt.Sprintf("error: tool %q not found", tc.Function.Name))
			continue
		}

		// Security: Check if action requires human approval
		if tool.RequiresApproval() {
			appID := "untracked"
			if a.ApprovalHandler != nil {
				appID = a.ApprovalHandler.Submit(a.Config.Name, tc.Function.Name, tc.Function.Arguments)
			}
			if out != nil {
				sendChunk(ctx, out, llm.StreamChunk{
					ToolCalls: []llm.ToolCall{tc},
					Content:   fmt.Sprintf("\n\n> ⚠️ **Action Submitted for Approval**: `%s` (ID: `%s`)\n", tc.Function.Name, appID),
				})
			}
			reply(fmt.Sprintf("Action pending human approval (ID: %s). The user will review and execute this action via the UI.", appID))
			continue
		}

		if out != nil {
			sendChunk(ctx, out, llm.StreamChunk{
				Content: fmt.Sprintf("\n*⚙️ Running tool: `%s`...*\n", tc.Function.Name),
			})
		}

		result, err := tool.Execute(ctx, json.RawMessage(tc.Function.Arguments))
		var content string
		switch {
		case err != nil:
			content = fmt.Sprintf("error: %v", err)
		case result.Error != "":
			content = "error: " + result.Error
		default:
			// Raw text, not JSON, so artifact line ranges match the real output.
			content = result.Output
		}
		source := fmt.Sprintf("%s(%s)", tc.Function.Name, tc.Function.Arguments)
		step = append(step, a.Memory.ToolResult(tc.ID, source, content))
	}
	a.Memory.AddStep(step...)
}

// Run executes the agent loop synchronously until a final response is ready.
func (a *Agent) Run(ctx context.Context, userMessage string) (string, error) {
	a.Memory.Begin(userMessage)
	toolDefs := a.toolDefs()

	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		resp, err := a.Engine.Generate(ctx, a.nextRequest(ctx, toolDefs))
		if err != nil {
			return "", fmt.Errorf("generation failed: %w", err)
		}

		if len(resp.Message.ToolCalls) == 0 {
			a.Memory.AddStep(resp.Message)
			return resp.Message.Content, nil
		}
		a.runTools(ctx, resp.Message, nil)
	}

	return "", fmt.Errorf("max iterations (%d) exceeded", maxIterations)
}

// RunStream executes the agent loop, streaming the final response to the provided channel.
func (a *Agent) RunStream(ctx context.Context, userMessage string, out chan<- llm.StreamChunk) error {
	defer close(out)

	for _, rule := range a.Memory.Begin(userMessage) {
		sendChunk(ctx, out, llm.StreamChunk{Content: fmt.Sprintf("> 📌 You've said this before, so it's now a standing rule in MEMORY.md: *%s*\n\n", rule)})
	}
	toolDefs := a.toolDefs()

	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req := a.nextRequest(ctx, toolDefs)

		// First, do a non-streaming check for tool calls
		resp, err := a.Engine.Generate(ctx, req)
		if err != nil {
			sendChunk(ctx, out, llm.StreamChunk{Error: fmt.Errorf("generation failed: %w", err)})
			return err
		}

		if len(resp.Message.ToolCalls) > 0 {
			a.runTools(ctx, resp.Message, out)
			continue
		}

		// No tool calls — stream the final assistant response
		req.Stream = true
		return a.Engine.Stream(ctx, req, out)
	}

	return fmt.Errorf("max iterations (%d) exceeded", maxIterations)
}
