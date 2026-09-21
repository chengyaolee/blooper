package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/tools"
)

const (
	maxIterations     = 20
	maxHistoryLength  = 16 // Sliding window to prevent token overflow & memory leaks
)

// ApprovalSubmitter is an interface for queueing actions that require human sign-off.
type ApprovalSubmitter interface {
	Submit(agentName, toolName, arguments string) string
}

// Agent represents an executing agent instance.
type Agent struct {
	Config              *AgentConfig
	Engine              llm.InferenceEngine
	ToolRegistry        *tools.ToolRegistry
	ApprovalHandler     ApprovalSubmitter
	conversationHistory []llm.ChatMessage
}

// NewAgent creates a new Agent instance.
func NewAgent(cfg *AgentConfig, engine llm.InferenceEngine, registry *tools.ToolRegistry) *Agent {
	return &Agent{
		Config:       cfg,
		Engine:       engine,
		ToolRegistry: registry,
	}
}

// SetApprovalHandler configures the approval handler for the agent.
func (a *Agent) SetApprovalHandler(handler ApprovalSubmitter) {
	a.ApprovalHandler = handler
}

// trimHistory bounds conversation memory to prevent token window overflow.
func (a *Agent) trimHistory() {
	if len(a.conversationHistory) <= maxHistoryLength {
		return
	}
	// Keep system prompt at index 0, and keep the latest (maxHistoryLength-1) messages
	trimmed := make([]llm.ChatMessage, 0, maxHistoryLength)
	trimmed = append(trimmed, a.conversationHistory[0])
	startIdx := len(a.conversationHistory) - (maxHistoryLength - 1)
	trimmed = append(trimmed, a.conversationHistory[startIdx:]...)
	a.conversationHistory = trimmed
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

// Run executes the agent loop synchronously until a final response is ready.
func (a *Agent) Run(ctx context.Context, userMessage string) (string, error) {
	if len(a.conversationHistory) == 0 {
		a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
			Role:    "system",
			Content: a.Config.Persona.SystemPrompt,
		})
	}

	a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
		Role:    "user",
		Content: userMessage,
	})
	a.trimHistory()

	toolDefs := a.ToolRegistry.ToToolDefs()

	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		req := &llm.ChatRequest{
			Model:       a.Config.Model.Preferred,
			Messages:    a.conversationHistory,
			Tools:       toolDefs,
			Temperature: a.Config.Model.Temperature,
		}

		resp, err := a.Engine.Generate(ctx, req)
		if err != nil {
			return "", fmt.Errorf("generation failed: %w", err)
		}

		a.conversationHistory = append(a.conversationHistory, resp.Message)
		a.trimHistory()

		if len(resp.Message.ToolCalls) == 0 {
			return resp.Message.Content, nil
		}

		for _, tc := range resp.Message.ToolCalls {
			tool, ok := a.ToolRegistry.Get(tc.Function.Name)
			if !ok {
				a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("error: tool %q not found", tc.Function.Name),
				})
				continue
			}

			// Security: Check if action requires human approval
			if tool.RequiresApproval() {
				appID := "untracked"
				if a.ApprovalHandler != nil {
					appID = a.ApprovalHandler.Submit(a.Config.Name, tc.Function.Name, tc.Function.Arguments)
				}
				a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("Action pending human approval (ID: %s). The user will review and execute this action via the UI.", appID),
				})
				continue
			}

			result, err := tool.Execute(ctx, json.RawMessage(tc.Function.Arguments))
			content := ""
			if err != nil {
				content = fmt.Sprintf("error: %v", err)
			} else {
				b, _ := json.Marshal(result)
				content = string(b)
			}

			a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})
			a.trimHistory()
		}
	}

	return "", fmt.Errorf("max iterations (%d) exceeded", maxIterations)
}

// RunStream executes the agent loop, streaming the final response to the provided channel.
func (a *Agent) RunStream(ctx context.Context, userMessage string, out chan<- llm.StreamChunk) error {
	defer close(out)

	if len(a.conversationHistory) == 0 {
		a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
			Role:    "system",
			Content: a.Config.Persona.SystemPrompt,
		})
	}

	a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
		Role:    "user",
		Content: userMessage,
	})
	a.trimHistory()

	toolDefs := a.ToolRegistry.ToToolDefs()

	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req := &llm.ChatRequest{
			Model:       a.Config.Model.Preferred,
			Messages:    a.conversationHistory,
			Tools:       toolDefs,
			Temperature: a.Config.Model.Temperature,
		}

		// First, do a non-streaming check for tool calls
		resp, err := a.Engine.Generate(ctx, req)
		if err != nil {
			sendChunk(ctx, out, llm.StreamChunk{Error: fmt.Errorf("generation failed: %w", err)})
			return err
		}

		if len(resp.Message.ToolCalls) > 0 {
			a.conversationHistory = append(a.conversationHistory, resp.Message)
			a.trimHistory()

			for _, tc := range resp.Message.ToolCalls {
				tool, ok := a.ToolRegistry.Get(tc.Function.Name)
				if !ok {
					a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
						Role:       "tool",
						ToolCallID: tc.ID,
						Content:    fmt.Sprintf("error: tool %q not found", tc.Function.Name),
					})
					continue
				}

				// Security check: approval required?
				if tool.RequiresApproval() {
					appID := "untracked"
					if a.ApprovalHandler != nil {
						appID = a.ApprovalHandler.Submit(a.Config.Name, tc.Function.Name, tc.Function.Arguments)
					}
					// Send approval chunk to UI stream
					sendChunk(ctx, out, llm.StreamChunk{
						ToolCalls: []llm.ToolCall{tc},
						Content:   fmt.Sprintf("\n\n> ⚠️ **Action Submitted for Approval**: `%s` (ID: `%s`)\n", tc.Function.Name, appID),
					})

					a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
						Role:       "tool",
						ToolCallID: tc.ID,
						Content:    fmt.Sprintf("Action paused for approval (ID: %s). Waiting for human review.", appID),
					})
					continue
				}

				// Notify UI that a tool is running
				sendChunk(ctx, out, llm.StreamChunk{
					Content: fmt.Sprintf("\n*⚙️ Running tool: `%s`...*\n", tc.Function.Name),
				})

				result, err := tool.Execute(ctx, json.RawMessage(tc.Function.Arguments))
				content := ""
				if err != nil {
					content = fmt.Sprintf("error: %v", err)
				} else {
					b, _ := json.Marshal(result)
					content = string(b)
				}

				a.conversationHistory = append(a.conversationHistory, llm.ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    content,
				})
				a.trimHistory()
			}
			continue
		}

		// No tool calls — stream the final assistant response
		req.Stream = true
		return a.Engine.Stream(ctx, req, out)
	}

	return fmt.Errorf("max iterations (%d) exceeded", maxIterations)
}
