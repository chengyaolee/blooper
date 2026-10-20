package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/tools"
	"github.com/chengyaolee/blooper/internal/trace"
)

const (
	maxSwarmRounds    = 5  // passes over the workers while claimable tasks remain
	maxTasksPerWorker = 10 // agent runs per worker per round
)

// Tools swarm agents must not see: run_goal would nest swarms, and delegate would do work
// off the board where it is neither claimed, verified nor attributed.
var swarmExcludedTools = []string{"run_goal", "delegate"}

// Swarm runs a goal with a controller agent and a team of worker agents that coordinate
// through the shared task graph in memory.Store: the controller splits the goal once,
// workers then choose and complete tasks on their own, and each result is verified as soon
// as it is reported - first by its deterministic acceptance checks, then by the controller.
// Every decision is a CTDG event, every agent run is a traced trajectory, and the finished
// trace is formally checked against the protocol (memory.CheckTrace).
type Swarm struct {
	store             *memory.Store
	agents            *agent.AgentRegistry
	engine            llm.InferenceEngine
	toolRegistry      *tools.ToolRegistry
	approvals         agent.ApprovalSubmitter
	defaultController string
	maxParallel       int
	OutputDir         string // base directory for file_exists checks
}

func NewSwarm(store *memory.Store, agents *agent.AgentRegistry, engine llm.InferenceEngine, toolReg *tools.ToolRegistry,
	approvals agent.ApprovalSubmitter, defaultController string, maxParallel int) *Swarm {
	return &Swarm{
		store:             store,
		agents:            agents,
		engine:            engine,
		toolRegistry:      toolReg,
		approvals:         approvals,
		defaultController: defaultController,
		maxParallel:       maxParallel,
		OutputDir:         "data/outputs",
	}
}

// goalRun is the state of one RunGoal call.
type goalRun struct {
	*Swarm
	goalID, goal string
	ctrl         *agent.AgentConfig
	workers      []*agent.AgentConfig
	workerNames  []string
}

// RunGoal satisfies tools.GoalRunner.
func (s *Swarm) RunGoal(ctx context.Context, goal, controller string, workerNames []string) (out string, err error) {
	if controller == "" {
		controller = s.defaultController
	}
	ctrl, ok := s.agents.Get(controller)
	if !ok {
		return "", fmt.Errorf("controller agent %q not found", controller)
	}
	workers, err := s.resolveWorkers(controller, workerNames)
	if err != nil {
		return "", err
	}
	g := &goalRun{Swarm: s, goal: goal, ctrl: ctrl, workers: workers}
	for _, w := range workers {
		g.workerNames = append(g.workerNames, w.Name)
	}

	// One parent run per goal, so every plan/work/verify run of the episode nests under it.
	ctx, goalTrace := trace.Start(ctx, s.store, trace.Info{Agent: controller, Role: "goal", Model: ctrl.Model.Preferred, Input: goal, Config: ctrl})
	defer func() { goalTrace.End(out, err) }()

	g.goalID, err = s.store.CreateGoal(goal, goalTrace.Actor(controller), g.workerNames, map[string]any{
		"controller_model": ctrl.Model.Preferred, "max_parallel": s.maxParallel, "max_rounds": maxSwarmRounds,
		"max_tasks_per_worker": maxTasksPerWorker, "max_attempts": memory.MaxAttempts,
	})
	if err != nil {
		return "", err
	}

	final, runErr := g.execute(ctx)

	// Always close the episode and verify its trace, even when the run failed part-way.
	score, err := s.store.FinishGoal(g.goalID, final)
	if err != nil {
		return "", err
	}
	report, err := s.store.VerifyTrace(g.goalID)
	if err != nil {
		return "", err
	}
	if err := s.store.SaveVerification(g.goalID, report); err != nil {
		return "", err
	}
	if runErr != nil {
		return "", fmt.Errorf("goal %s: %w", g.goalID, runErr)
	}

	verdict := "protocol trace verified"
	if !report.OK {
		verdict = fmt.Sprintf("protocol trace has %d safety, %d liveness, %d projection violation(s)",
			len(report.Safety), len(report.Liveness), len(report.Projection))
	}
	return fmt.Sprintf("%s\n\n(goal %s: %d/%d subtasks verified, %d auto-approved, %d failed, %d unfinished; %s)",
		final, g.goalID, score.Approved, score.TotalLeaves, score.AutoApproved, score.Failed, score.Unfinished, verdict), nil
}

// execute plans, lets the workers self-organize, verifies stragglers and synthesizes the answer.
func (g *goalRun) execute(ctx context.Context) (string, error) {
	// 1. Initial split. If the controller doesn't decompose, the root stays a single claimable task.
	if _, _, err := g.run(ctx, g.ctrl, "plan", "", true, planPrompt(g.goal, g.workers)); err != nil {
		return "", fmt.Errorf("planning failed: %w", err)
	}

	// 2. Workers choose tasks until nothing is left to claim.
	for round := 0; round < maxSwarmRounds; round++ {
		sched := NewScheduler(g.maxParallel)
		started := 0
		for _, w := range g.workers {
			if ids, _ := g.store.Claimable(g.goalID, w.Name); len(ids) == 0 {
				continue
			}
			w := w
			if err := sched.Submit(ctx, func(ctx context.Context) { g.workerLoop(ctx, w) }); err != nil {
				break
			}
			started++
		}
		sched.Wait()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if started == 0 {
			break
		}
	}

	// 3. Retry verification for anything still awaiting a verdict (e.g. after a model error).
	g.verifyPending(ctx, "")

	// 4. Controller synthesizes the final answer from the verified tree.
	tree, err := g.store.RenderTree(g.goalID)
	if err != nil {
		return "", err
	}
	final, _, err := g.run(ctx, g.ctrl, "synthesize", "", true, synthesizePrompt(g.goal, tree))
	if err != nil {
		return "", fmt.Errorf("synthesis failed: %w", err)
	}
	return final, nil
}

func (s *Swarm) resolveWorkers(controller string, names []string) ([]*agent.AgentConfig, error) {
	if len(names) == 0 {
		for _, cfg := range s.agents.List() {
			if cfg.Name != controller {
				names = append(names, cfg.Name)
			}
		}
		if len(names) == 0 {
			names = []string{controller}
		}
	}
	var workers []*agent.AgentConfig
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		cfg, ok := s.agents.Get(n)
		if !ok {
			return nil, fmt.Errorf("worker agent %q not found", n)
		}
		workers = append(workers, cfg)
	}
	slices.SortFunc(workers, func(a, b *agent.AgentConfig) int { return strings.Compare(a.Name, b.Name) })
	return workers, nil
}

// workerLoop lets one agent keep choosing tasks until none are left for it or it stops making progress.
func (g *goalRun) workerLoop(ctx context.Context, w *agent.AgentConfig) {
	for i := 0; i < maxTasksPerWorker && ctx.Err() == nil; i++ {
		ids, err := g.store.Claimable(g.goalID, w.Name)
		if err != nil || len(ids) == 0 {
			return
		}
		tree, err := g.store.RenderTree(g.goalID)
		if err != nil {
			return
		}

		_, runID, runErr := g.run(ctx, w, "work", "", false, workPrompt(w.Name, g.goal, tree, ids))
		_, _ = g.store.ReleaseClaims(g.goalID, memory.Actor{Agent: w.Name, RunID: runID})
		g.verifyPending(ctx, w.Name)

		if runErr != nil {
			fmt.Printf("⚠️  goal %s: worker %s stopped: %v\n", g.goalID, w.Name, runErr)
			return
		}
		// Progress means this run delivered or split work; claiming then abandoning doesn't count.
		if n, err := g.store.RunEventCount(runID, "completed", "decomposed"); err != nil || n == 0 {
			return
		}
	}
}

// verifyPending verifies every done task (of one worker, or all when worker is "").
func (g *goalRun) verifyPending(ctx context.Context, worker string) {
	tasks, err := g.store.Tasks(g.goalID)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.Status == memory.StatusDone && (worker == "" || t.Owner == worker) {
			g.verify(ctx, t)
		}
	}
}

// verify runs the task's deterministic checks, then asks the controller for a verdict.
func (g *goalRun) verify(ctx context.Context, t memory.Task) {
	var results []memory.CheckResult
	if len(t.Checks) > 0 {
		results = RunChecks(t.Checks, t.Result, g.OutputDir)
		if err := g.store.RecordChecks(g.goalID, t.ID, results); err != nil {
			return // no longer done: someone else already verified it
		}
		if failed := failedChecks(results); failed != "" {
			_ = g.store.Verify(g.goalID, t.ID, memory.System, false, "acceptance checks failed: "+failed)
			return
		}
	}

	_, runID, runErr := g.run(ctx, g.ctrl, "verify", t.ID, true, verifyPrompt(g.goal, t, results))
	cur, err := g.store.Task(g.goalID, t.ID)
	if err != nil || cur.Status != memory.StatusDone || cur.Owner != t.Owner || cur.Attempts != t.Attempts {
		return // the controller gave a verdict (or the task moved on)
	}
	if runErr != nil {
		// No verdict because the model call failed: never approve. The task stays done and is
		// retried by the final sweep; if that fails too the episode reports it as unfinished.
		_ = g.store.RecordVerificationError(g.goalID, t.ID, memory.Actor{Agent: g.ctrl.Name, RunID: runID}, runErr)
		return
	}
	// The controller ran but skipped the verdict: approve under System so it is excluded from the score.
	_ = g.store.Verify(g.goalID, t.ID, memory.System, true, "auto-approved: controller gave no verdict")
}

// run executes a fresh, traced agent instance with the shared tools plus a taskboard bound to
// this goal and run.
func (g *goalRun) run(ctx context.Context, cfg *agent.AgentConfig, role, taskID string, controller bool, prompt string) (string, string, error) {
	ctx, run := trace.Start(ctx, g.store, trace.Info{
		Agent: cfg.Name, Role: role, Model: cfg.Model.Preferred, GoalID: g.goalID, TaskID: taskID, Input: prompt, Config: cfg,
	})

	reg := tools.NewRegistry()
	if g.toolRegistry != nil {
		for _, t := range g.toolRegistry.List() {
			if !slices.Contains(swarmExcludedTools, t.Name()) {
				reg.Register(t)
			}
		}
	}
	reg.Register(trace.Tool(tools.NewTaskBoardTool(g.store, g.goalID, run.Actor(cfg.Name), controller, g.workerNames)))

	a := agent.NewAgent(cfg, g.engine, reg)
	if g.approvals != nil {
		a.SetApprovalHandler(g.approvals)
	}
	out, err := a.Run(ctx, prompt)
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	run.End(out, err)
	return out, run.RunID(), err
}

func planPrompt(goal string, workers []*agent.AgentConfig) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are the controller for this goal:\n%s\n\nWorker agents available:\n", goal)
	for _, w := range workers {
		fmt.Fprintf(&sb, "- %s: %s\n", w.Name, w.Description)
	}
	kinds := make([]string, 0, len(memory.CheckKinds))
	for k := range memory.CheckKinds {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	fmt.Fprintf(&sb, `
Break the goal into a small number of concrete, independently executable subtasks.
Call the taskboard tool with action "decompose", task_id "1" and the subtasks.
Optionally set "assignee" on a subtask to reserve it for the best-suited worker (use the exact names above).
Give each subtask acceptance "checks" where the outcome can be verified mechanically
(kinds: %s), e.g. {"kind":"contains","arg":"https://"} for a cited result.
Do NOT execute the subtasks yourself. Reply with a one-line summary when done.`, strings.Join(kinds, ", "))
	return sb.String()
}

func workPrompt(name, goal, tree string, claimable []string) string {
	return fmt.Sprintf(`You are %s, one of several agents working together on this goal:
%s

Current task board:
%s
Tasks you can claim: %s

Pick the single task you are best suited for, then:
1. Call taskboard with action "claim" and its task_id.
2. Do the work with your tools. If the task is too large, instead call taskboard "decompose" on it and stop.
3. Call taskboard with action "complete", the task_id and the full result. The result must satisfy the task's acceptance checks.
Reply with a one-line summary when done.`, name, goal, tree, strings.Join(claimable, ", "))
}

func verifyPrompt(goal string, t memory.Task, checks []memory.CheckResult) string {
	checkNote := ""
	if len(checks) > 0 {
		checkNote = fmt.Sprintf("\nAll %d automatic acceptance checks passed; judge what they cannot measure.\n", len(checks))
	}
	return fmt.Sprintf(`You are verifying work on this goal:
%s

Task [%s] %s
%s
Completed by %s. Result:
%s
%s
Check that the result fully and correctly accomplishes the task.
Call taskboard with action "verify", task_id "%s", approved true or false, and feedback (required when rejecting).`,
		goal, t.ID, t.Title, t.Description, t.Owner, t.Result, checkNote, t.ID)
}

func synthesizePrompt(goal, tree string) string {
	return fmt.Sprintf(`All work on this goal has finished:
%s

Final task board:
%s
Write the final answer to the goal using the approved results. Mention any parts that failed.`, goal, tree)
}
