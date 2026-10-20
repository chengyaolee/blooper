package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
)

// TaskBoardTool is an agent's view of a shared goal's task graph, bound to one goal and one actor.
// Agents coordinate only through this board: every call is recorded as a CTDG edge.
type TaskBoardTool struct {
	Store      *memory.Store
	GoalID     string
	Actor      memory.Actor
	Controller bool     // the controller plans and verifies; it never claims or completes work
	Workers    []string // valid assignees for decompose
}

func NewTaskBoardTool(store *memory.Store, goalID string, actor memory.Actor, controller bool, workers []string) *TaskBoardTool {
	return &TaskBoardTool{Store: store, GoalID: goalID, Actor: actor, Controller: controller, Workers: workers}
}

func (t *TaskBoardTool) Name() string { return "taskboard" }

func (t *TaskBoardTool) Description() string {
	return "Shared task tree for the current goal. Actions: 'view' (see all tasks, statuses and results), " +
		"'decompose' (split a task into subtasks), 'claim' (take an open task), 'complete' (report your result), " +
		"'verify' (controller only: approve or reject a completed task)."
}

func (t *TaskBoardTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type": "string",
					"enum": []string{"view", "decompose", "claim", "complete", "verify"},
				},
				"task_id": map[string]interface{}{
					"type":        "string",
					"description": "Task ID such as '1' or '1.2' (required for all actions except view)",
				},
				"subtasks": map[string]interface{}{
					"type":        "array",
					"description": "For decompose: the subtasks to create",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"title":       map[string]interface{}{"type": "string"},
							"description": map[string]interface{}{"type": "string"},
							"assignee":    map[string]interface{}{"type": "string", "description": "Optional agent name to reserve this subtask for"},
							"checks": map[string]interface{}{
								"type":        "array",
								"description": "Optional machine-checked acceptance criteria. " + checkKindsHelp(),
								"items": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"kind": map[string]interface{}{"type": "string"},
										"arg":  map[string]interface{}{"type": "string"},
									},
									"required": []string{"kind"},
								},
							},
						},
						"required": []string{"title"},
					},
				},
				"result": map[string]interface{}{
					"type":        "string",
					"description": "For complete: the full output of the task",
				},
				"approved": map[string]interface{}{
					"type":        "boolean",
					"description": "For verify: true to accept the result, false to send it back",
				},
				"feedback": map[string]interface{}{
					"type":        "string",
					"description": "For verify: what is wrong or what to improve",
				},
			},
			"required": []string{"action"},
		},
	}
}

func (t *TaskBoardTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var p struct {
		Action   string               `json:"action"`
		TaskID   string               `json:"task_id"`
		Subtasks []memory.SubtaskSpec `json:"subtasks"`
		Result   string               `json:"result"`
		Approved *bool                `json:"approved"`
		Feedback string               `json:"feedback"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return &ToolResult{Error: "invalid arguments"}, nil
	}
	if p.Action != "view" && p.TaskID == "" {
		return &ToolResult{Error: "task_id is required"}, nil
	}

	var err error
	switch p.Action {
	case "view":
		tree, err := t.Store.RenderTree(t.GoalID)
		if err != nil {
			return &ToolResult{Error: err.Error()}, nil
		}
		claimable, _ := t.Store.Claimable(t.GoalID, t.Actor.Agent)
		return &ToolResult{Output: fmt.Sprintf("%s\nClaimable by you: %s", tree, strings.Join(claimable, ", "))}, nil
	case "decompose":
		if err := t.checkDecompose(p.TaskID, p.Subtasks); err != nil {
			return &ToolResult{Error: err.Error()}, nil
		}
		var ids []string
		if ids, err = t.Store.Decompose(t.GoalID, p.TaskID, t.Actor, p.Subtasks); err == nil {
			return &ToolResult{Output: fmt.Sprintf("Created subtasks %s under %s.", strings.Join(ids, ", "), p.TaskID)}, nil
		}
	case "claim", "complete":
		if t.Controller {
			return &ToolResult{Error: "the controller plans and verifies; workers claim and complete tasks"}, nil
		}
		if p.Action == "claim" {
			if err = t.Store.Claim(t.GoalID, p.TaskID, t.Actor); err == nil {
				return &ToolResult{Output: fmt.Sprintf("You own task %s. Do it, then call complete with the result.", p.TaskID)}, nil
			}
		} else if err = t.Store.Complete(t.GoalID, p.TaskID, t.Actor, p.Result); err == nil {
			return &ToolResult{Output: fmt.Sprintf("Task %s reported as done and sent for verification.", p.TaskID)}, nil
		}
	case "verify":
		if !t.Controller {
			return &ToolResult{Error: "only the controller can verify tasks"}, nil
		}
		if p.Approved == nil {
			return &ToolResult{Error: "verify needs approved: true or false"}, nil
		}
		if !*p.Approved && strings.TrimSpace(p.Feedback) == "" {
			return &ToolResult{Error: "feedback is required when rejecting"}, nil
		}
		if err = t.Store.Verify(t.GoalID, p.TaskID, t.Actor, *p.Approved, p.Feedback); err == nil {
			return &ToolResult{Output: fmt.Sprintf("Verdict recorded for task %s (approved=%v).", p.TaskID, *p.Approved)}, nil
		}
	default:
		return &ToolResult{Error: fmt.Sprintf("unknown action %q", p.Action)}, nil
	}
	return &ToolResult{Error: err.Error()}, nil
}

// checkDecompose enforces who may split what, and that assignees are real workers.
func (t *TaskBoardTool) checkDecompose(taskID string, subtasks []memory.SubtaskSpec) error {
	if !t.Controller {
		task, err := t.Store.Task(t.GoalID, taskID)
		if err != nil {
			return err
		}
		if task.Status != memory.StatusClaimed || task.Owner != t.Actor.Agent {
			return fmt.Errorf("claim task %s before decomposing it", taskID)
		}
	}
	for _, sub := range subtasks {
		if sub.Assignee != "" && len(t.Workers) > 0 && !slices.Contains(t.Workers, sub.Assignee) {
			return fmt.Errorf("unknown assignee %q for %q; valid workers: %s", sub.Assignee, sub.Title, strings.Join(t.Workers, ", "))
		}
	}
	return nil
}

func checkKindsHelp() string {
	kinds := make([]string, 0, len(memory.CheckKinds))
	for k, desc := range memory.CheckKinds {
		kinds = append(kinds, k+": "+desc)
	}
	slices.Sort(kinds)
	return "Kinds - " + strings.Join(kinds, "; ")
}

func (t *TaskBoardTool) RequiresApproval() bool { return false }

// GoalRunner runs a goal through a controller and a team of worker agents.
type GoalRunner interface {
	RunGoal(ctx context.Context, goal, controller string, workers []string) (string, error)
}

// RunGoalTool lets an agent hand a multi-step goal to a self-organizing team of agents.
type RunGoalTool struct {
	Runner GoalRunner
}

func NewRunGoalTool(runner GoalRunner) *RunGoalTool {
	return &RunGoalTool{Runner: runner}
}

func (t *RunGoalTool) Name() string { return "run_goal" }

func (t *RunGoalTool) Description() string {
	return "Runs a large multi-step goal with a team of agents: a controller splits it into a task tree, " +
		"worker agents pick and complete subtasks, and the controller verifies each result. Returns the final answer."
}

func (t *RunGoalTool) Schema() llm.FunctionDef {
	return llm.FunctionDef{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"goal": map[string]interface{}{
					"type":        "string",
					"description": "The overall goal, with all context the team needs",
				},
				"controller": map[string]interface{}{
					"type":        "string",
					"description": "Optional agent that plans and verifies (defaults to the default agent)",
				},
				"workers": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Optional agent names that execute subtasks (defaults to all agents)",
				},
			},
			"required": []string{"goal"},
		},
	}
}

func (t *RunGoalTool) Execute(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	if t.Runner == nil {
		return &ToolResult{Error: "goal runner is uninitialized"}, nil
	}
	var p struct {
		Goal       string   `json:"goal"`
		Controller string   `json:"controller"`
		Workers    []string `json:"workers"`
	}
	if err := json.Unmarshal(args, &p); err != nil || p.Goal == "" {
		return &ToolResult{Error: "invalid arguments: goal is required"}, nil
	}
	out, err := t.Runner.RunGoal(ctx, p.Goal, p.Controller, p.Workers)
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("goal failed: %v", err)}, nil
	}
	return &ToolResult{Output: out}, nil
}

func (t *RunGoalTool) RequiresApproval() bool { return false }
