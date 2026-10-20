package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// The task graph is a Continuous-Time Dynamic Graph (CTDG): nodes are agents and tasks,
// and every decision is an append-only timestamped edge (src, dst, ts, kind, payload) in
// task_events. The tasks table is the materialized current state of that stream, updated in
// the same transaction, and is what agents read to coordinate. task_events doubles as the
// RL dataset: claim edges carry the action space ("options"), verification edges carry the
// reward, goal_completed carries the episode return, and run_id joins each edge to the agent
// run (and its LLM/tool steps) that produced it. VerifyTrace replays the stream against the
// protocol to prove the materialized state is consistent and no rule was broken.
const taskGraphSchema = `
	CREATE TABLE IF NOT EXISTS goals (
		id TEXT PRIMARY KEY,
		goal TEXT NOT NULL,
		controller TEXT NOT NULL,
		run_id TEXT NOT NULL DEFAULT '',
		workers TEXT NOT NULL DEFAULT '[]',
		settings TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL,
		score REAL,
		final TEXT NOT NULL DEFAULT '',
		verification TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		ended_at INTEGER
	);
	CREATE TABLE IF NOT EXISTS tasks (
		goal_id TEXT NOT NULL,
		id TEXT NOT NULL,
		parent_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		checks TEXT NOT NULL DEFAULT '[]',
		status TEXT NOT NULL,
		assignee TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '',
		feedback TEXT NOT NULL DEFAULT '',
		verified_by TEXT NOT NULL DEFAULT '',
		attempts INTEGER NOT NULL DEFAULT 0,
		created_by TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (goal_id, id)
	);
	CREATE TABLE IF NOT EXISTS task_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		goal_id TEXT NOT NULL,
		ts INTEGER NOT NULL,
		src TEXT NOT NULL,
		dst TEXT NOT NULL,
		kind TEXT NOT NULL,
		reward REAL,
		payload TEXT NOT NULL DEFAULT '{}',
		run_id TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS task_events_goal ON task_events(goal_id, id);
`

// Task statuses.
const (
	StatusOpen       = "open"       // claimable
	StatusClaimed    = "claimed"    // an agent is working on it
	StatusDone       = "done"       // result reported, awaiting verification
	StatusApproved   = "approved"   // verified (or all children approved)
	StatusFailed     = "failed"     // rejected MaxAttempts times
	StatusDecomposed = "decomposed" // split into children; approved when they all are
)

// MaxAttempts is how many rejected results a task tolerates before it is marked failed.
const MaxAttempts = 3

// SystemActor is the src of edges emitted by bookkeeping rather than an agent decision.
const SystemActor = "system"

// RootTaskID is the ID of the goal's root task. Children are "1.1", "1.2", "1.2.1", ...
const RootTaskID = "1"

// Actor identifies who made a decision: the agent (CTDG src node) and the run it happened in.
type Actor struct {
	Agent string
	RunID string
}

// System is the bookkeeping actor.
var System = Actor{Agent: SystemActor}

// Check is a machine-verifiable acceptance criterion attached to a task at decomposition.
type Check struct {
	Kind string `json:"kind"`
	Arg  string `json:"arg,omitempty"`
}

// CheckKinds lists the supported check kinds and what they assert.
var CheckKinds = map[string]string{
	"contains":    "result contains arg (case-insensitive)",
	"regex":       "result matches the regular expression arg",
	"min_length":  "result has at least arg characters",
	"max_length":  "result has at most arg characters",
	"json":        "result is valid JSON",
	"file_exists": "file arg exists under the outputs directory and is non-empty",
}

// Validate rejects checks that could never be evaluated.
func (c Check) Validate() error {
	if _, ok := CheckKinds[c.Kind]; !ok {
		return fmt.Errorf("unknown check kind %q", c.Kind)
	}
	switch c.Kind {
	case "contains", "file_exists":
		if strings.TrimSpace(c.Arg) == "" {
			return fmt.Errorf("check %s needs an arg", c.Kind)
		}
	case "regex":
		if _, err := regexp.Compile(c.Arg); err != nil {
			return fmt.Errorf("check regex: %w", err)
		}
	case "min_length", "max_length":
		if n, err := strconv.Atoi(c.Arg); err != nil || n < 0 {
			return fmt.Errorf("check %s needs a non-negative integer arg", c.Kind)
		}
	}
	return nil
}

// CheckResult is the outcome of evaluating one Check.
type CheckResult struct {
	Check
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// Task is a node in a goal's task tree.
type Task struct {
	GoalID      string
	ID          string
	ParentID    string
	Title       string
	Description string
	Checks      []Check
	Status      string
	Assignee    string // delegated to (set at decomposition); empty = anyone may claim
	Owner       string // agent currently holding / last completing the task
	Result      string
	Feedback    string // latest verifier feedback
	VerifiedBy  string // who issued the latest verdict
	Attempts    int    // rejected results so far
	CreatedBy   string
	UpdatedAt   int64
}

// Event is one timestamped edge of the CTDG.
type Event struct {
	ID      int64           `json:"id"`
	GoalID  string          `json:"goal_id"`
	TS      int64           `json:"ts"` // unix microseconds
	Src     string          `json:"src"`
	Dst     string          `json:"dst"`
	Kind    string          `json:"kind"`
	Reward  *float64        `json:"reward,omitempty"`
	Payload json.RawMessage `json:"payload"`
	RunID   string          `json:"run_id,omitempty"`
}

// SubtaskSpec describes a child task when decomposing.
type SubtaskSpec struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Assignee    string  `json:"assignee,omitempty"`
	Checks      []Check `json:"checks,omitempty"`
}

const taskCols = `goal_id, id, parent_id, title, description, checks, status, assignee, owner, result, feedback, verified_by, attempts, created_by, updated_at`

func (s *Store) withTx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

var lastTS atomic.Int64

// now returns a strictly increasing unix-microsecond timestamp, so event time never runs
// backwards even if the wall clock is adjusted.
func now() int64 {
	for {
		last, t := lastTS.Load(), time.Now().UnixMicro()
		if t <= last {
			t = last + 1
		}
		if lastTS.CompareAndSwap(last, t) {
			return t
		}
	}
}

func emit(tx *sql.Tx, goalID string, by Actor, dst, kind string, reward *float64, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to encode event payload: %w", err)
	}
	_, err = tx.Exec(`INSERT INTO task_events (goal_id, ts, src, dst, kind, reward, payload, run_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		goalID, now(), by.Agent, dst, kind, reward, string(b), by.RunID)
	if err != nil {
		return fmt.Errorf("failed to record %s event: %w", kind, err)
	}
	return nil
}

func scanTasks(rows *sql.Rows) ([]Task, error) {
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var checks string
		if err := rows.Scan(&t.GoalID, &t.ID, &t.ParentID, &t.Title, &t.Description, &checks, &t.Status, &t.Assignee,
			&t.Owner, &t.Result, &t.Feedback, &t.VerifiedBy, &t.Attempts, &t.CreatedBy, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan task: %w", err)
		}
		_ = json.Unmarshal([]byte(checks), &t.Checks)
		out = append(out, t)
	}
	return out, rows.Err()
}

func getTask(tx *sql.Tx, goalID, id string) (*Task, error) {
	rows, err := tx.Query(`SELECT `+taskCols+` FROM tasks WHERE goal_id = ? AND id = ?`, goalID, id)
	if err != nil {
		return nil, fmt.Errorf("failed to load task: %w", err)
	}
	tasks, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("task %s not found in goal %s", id, goalID)
	}
	return &tasks[0], nil
}

func claimableIDs(tx *sql.Tx, goalID, agent string) ([]string, error) {
	rows, err := tx.Query(`SELECT id FROM tasks WHERE goal_id = ? AND status = ? AND (assignee = '' OR assignee = ?) ORDER BY rowid`,
		goalID, StatusOpen, agent)
	if err != nil {
		return nil, fmt.Errorf("failed to list claimable tasks: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateGoal starts a new episode whose root task is the goal itself. settings is any
// JSON-encodable snapshot of the run configuration, kept for later analysis.
func (s *Store) CreateGoal(goal string, by Actor, workers []string, settings any) (string, error) {
	goalID := uuid.NewString()
	w, _ := json.Marshal(workers)
	cfg, _ := json.Marshal(settings)
	err := s.withTx(func(tx *sql.Tx) error {
		t := now()
		if _, err := tx.Exec(`INSERT INTO goals (id, goal, controller, run_id, workers, settings, status, started_at) VALUES (?, ?, ?, ?, ?, ?, 'running', ?)`,
			goalID, goal, by.Agent, by.RunID, string(w), string(cfg), t); err != nil {
			return fmt.Errorf("failed to create goal: %w", err)
		}
		if _, err := tx.Exec(`INSERT INTO tasks (goal_id, id, title, status, created_by, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			goalID, RootTaskID, goal, StatusOpen, by.Agent, t); err != nil {
			return fmt.Errorf("failed to create root task: %w", err)
		}
		return emit(tx, goalID, by, RootTaskID, "goal_created", nil, map[string]any{"goal": goal, "workers": workers})
	})
	return goalID, err
}

// Decompose splits a task into subtasks. The parent must be open, or claimed by the actor.
// Role checks (who may decompose open tasks, which assignees exist) belong to the caller.
func (s *Store) Decompose(goalID, parentID string, by Actor, subtasks []SubtaskSpec) ([]string, error) {
	if len(subtasks) == 0 {
		return nil, fmt.Errorf("at least one subtask is required")
	}
	for i, sub := range subtasks {
		if strings.TrimSpace(sub.Title) == "" {
			return nil, fmt.Errorf("subtask %d has no title", i+1)
		}
		for _, c := range sub.Checks {
			if err := c.Validate(); err != nil {
				return nil, fmt.Errorf("subtask %d: %w", i+1, err)
			}
		}
	}
	var ids []string
	err := s.withTx(func(tx *sql.Tx) error {
		parent, err := getTask(tx, goalID, parentID)
		if err != nil {
			return err
		}
		if parent.Status != StatusOpen && !(parent.Status == StatusClaimed && parent.Owner == by.Agent) {
			return fmt.Errorf("task %s is %s and cannot be decomposed by %s", parentID, parent.Status, by.Agent)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE goal_id = ? AND parent_id = ?`, goalID, parentID).Scan(&n); err != nil {
			return err
		}
		t := now()
		for i, sub := range subtasks {
			id := fmt.Sprintf("%s.%d", parentID, n+i+1)
			checks, _ := json.Marshal(append([]Check{}, sub.Checks...))
			if _, err := tx.Exec(`INSERT INTO tasks (goal_id, id, parent_id, title, description, checks, status, assignee, created_by, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, goalID, id, parentID, sub.Title, sub.Description, string(checks), StatusOpen, sub.Assignee, by.Agent, t); err != nil {
				return fmt.Errorf("failed to create subtask: %w", err)
			}
			if err := emit(tx, goalID, by, id, "decomposed", nil, map[string]any{
				"parent": parentID, "depth": strings.Count(id, "."), "title": sub.Title, "description": sub.Description,
				"checks": sub.Checks, "siblings": len(subtasks),
			}); err != nil {
				return err
			}
			if sub.Assignee != "" {
				if err := emit(tx, goalID, by, id, "assigned", nil, map[string]string{"assignee": sub.Assignee}); err != nil {
					return err
				}
			}
			ids = append(ids, id)
		}
		_, err = tx.Exec(`UPDATE tasks SET status = ?, updated_at = ? WHERE goal_id = ? AND id = ?`, StatusDecomposed, t, goalID, parentID)
		return err
	})
	return ids, err
}

// Claim lets an agent take an open task. It fails if someone else got there first.
func (s *Store) Claim(goalID, taskID string, by Actor) error {
	return s.withTx(func(tx *sql.Tx) error {
		options, err := claimableIDs(tx, goalID, by.Agent)
		if err != nil {
			return err
		}
		res, err := tx.Exec(`UPDATE tasks SET status = ?, owner = ?, updated_at = ?
			WHERE goal_id = ? AND id = ? AND status = ? AND (assignee = '' OR assignee = ?)`,
			StatusClaimed, by.Agent, now(), goalID, taskID, StatusOpen, by.Agent)
		if err != nil {
			return fmt.Errorf("failed to claim task: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("task %s is not claimable by %s (claimable: %v)", taskID, by.Agent, options)
		}
		return emit(tx, goalID, by, taskID, "claimed", nil, map[string]any{"options": options})
	})
}

// Complete reports a result for a task the actor has claimed. It then awaits verification.
func (s *Store) Complete(goalID, taskID string, by Actor, result string) error {
	return s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE tasks SET status = ?, result = ?, updated_at = ?
			WHERE goal_id = ? AND id = ? AND status = ? AND owner = ?`,
			StatusDone, result, now(), goalID, taskID, StatusClaimed, by.Agent)
		if err != nil {
			return fmt.Errorf("failed to complete task: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("task %s is not claimed by %s", taskID, by.Agent)
		}
		return emit(tx, goalID, by, taskID, "completed", nil, map[string]any{"result": result, "result_chars": len(result)})
	})
}

// RecordChecks logs the evaluation of a done task's acceptance checks.
func (s *Store) RecordChecks(goalID, taskID string, results []CheckResult) error {
	return s.withTx(func(tx *sql.Tx) error {
		t, err := getTask(tx, goalID, taskID)
		if err != nil {
			return err
		}
		if t.Status != StatusDone {
			return fmt.Errorf("task %s is %s, only done tasks can be checked", taskID, t.Status)
		}
		passed := 0
		for _, r := range results {
			if r.Passed {
				passed++
			}
		}
		return emit(tx, goalID, System, taskID, "checked", nil, map[string]any{
			"results": results, "passed": passed, "total": len(results), "attempt": t.Attempts + 1,
		})
	})
}

// RecordVerificationError logs that the verifier could not produce a verdict (e.g. the model
// call failed). The task stays done so it can be verified again.
func (s *Store) RecordVerificationError(goalID, taskID string, by Actor, cause error) error {
	return s.withTx(func(tx *sql.Tx) error {
		return emit(tx, goalID, by, taskID, "verification_error", nil, map[string]string{"error": cause.Error()})
	})
}

// Verify records a verdict on a completed task. Approval rolls up to parents; rejection
// reopens the task with feedback, or fails it after MaxAttempts. Verdicts by System are
// marked auto and excluded from the goal score.
func (s *Store) Verify(goalID, taskID string, by Actor, approved bool, feedback string) error {
	return s.withTx(func(tx *sql.Tx) error {
		t, err := getTask(tx, goalID, taskID)
		if err != nil {
			return err
		}
		if t.Status != StatusDone {
			return fmt.Errorf("task %s is %s, only done tasks can be verified", taskID, t.Status)
		}
		ts := now()
		verdict, reward := "approved", 1.0
		if !approved {
			verdict, reward = "rejected", -1.0
		}
		if err := emit(tx, goalID, by, taskID, "verified", &reward, map[string]any{
			"verdict": verdict, "feedback": feedback, "worker": t.Owner, "attempt": t.Attempts + 1,
			"result": t.Result, "system_verdict": by.Agent == SystemActor, // checks or no-verdict fallback, not an LLM/human judgment
		}); err != nil {
			return err
		}

		if approved {
			if _, err := tx.Exec(`UPDATE tasks SET status = ?, feedback = ?, verified_by = ?, updated_at = ? WHERE goal_id = ? AND id = ?`,
				StatusApproved, feedback, by.Agent, ts, goalID, taskID); err != nil {
				return err
			}
			return rollUp(tx, goalID, t.ParentID)
		}

		attempts := t.Attempts + 1
		if attempts >= MaxAttempts {
			if _, err := tx.Exec(`UPDATE tasks SET status = ?, feedback = ?, verified_by = ?, attempts = ?, updated_at = ? WHERE goal_id = ? AND id = ?`,
				StatusFailed, feedback, by.Agent, attempts, ts, goalID, taskID); err != nil {
				return err
			}
			return emit(tx, goalID, System, taskID, "failed", nil, map[string]int{"attempts": attempts})
		}
		if _, err := tx.Exec(`UPDATE tasks SET status = ?, owner = '', feedback = ?, verified_by = ?, attempts = ?, updated_at = ? WHERE goal_id = ? AND id = ?`,
			StatusOpen, feedback, by.Agent, attempts, ts, goalID, taskID); err != nil {
			return err
		}
		return emit(tx, goalID, by, taskID, "reopened", nil, map[string]any{"feedback": feedback, "attempts": attempts})
	})
}

// rollUp approves decomposed ancestors whose children are all approved.
func rollUp(tx *sql.Tx, goalID, parentID string) error {
	for parentID != "" {
		var pending int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE goal_id = ? AND parent_id = ? AND status != ?`,
			goalID, parentID, StatusApproved).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return nil
		}
		res, err := tx.Exec(`UPDATE tasks SET status = ?, verified_by = ?, updated_at = ? WHERE goal_id = ? AND id = ? AND status = ?`,
			StatusApproved, SystemActor, now(), goalID, parentID, StatusDecomposed)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if err := emit(tx, goalID, System, parentID, "rolled_up", nil, map[string]string{}); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT parent_id FROM tasks WHERE goal_id = ? AND id = ?`, goalID, parentID).Scan(&parentID); err != nil {
			return err
		}
	}
	return nil
}

// ReleaseClaims reopens tasks the agent claimed but never completed.
func (s *Store) ReleaseClaims(goalID string, by Actor) ([]string, error) {
	var released []string
	err := s.withTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM tasks WHERE goal_id = ? AND status = ? AND owner = ?`, goalID, StatusClaimed, by.Agent)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			released = append(released, id)
		}
		rows.Close()
		for _, id := range released {
			if _, err := tx.Exec(`UPDATE tasks SET status = ?, owner = '', updated_at = ? WHERE goal_id = ? AND id = ?`,
				StatusOpen, now(), goalID, id); err != nil {
				return err
			}
			if err := emit(tx, goalID, by, id, "abandoned", nil, map[string]string{}); err != nil {
				return err
			}
		}
		return nil
	})
	return released, err
}

// GoalScore summarizes leaf outcomes. Score counts only leaves approved by a real verifier.
type GoalScore struct {
	Score        float64 `json:"score"`
	TotalLeaves  int     `json:"total_leaves"`
	Approved     int     `json:"approved"`      // approved by the controller (or a deterministic check)
	AutoApproved int     `json:"auto_approved"` // approved by System without a verdict
	Failed       int     `json:"failed"`
	Unfinished   int     `json:"unfinished"` // open, claimed or done when the episode ended
}

func scoreGoal(tx *sql.Tx, goalID string) (GoalScore, error) {
	var g GoalScore
	err := tx.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(t.status = ? AND t.verified_by != ?), 0),
			COALESCE(SUM(t.status = ? AND t.verified_by = ?), 0),
			COALESCE(SUM(t.status = ?), 0)
		FROM tasks t
		WHERE t.goal_id = ? AND NOT EXISTS (SELECT 1 FROM tasks c WHERE c.goal_id = t.goal_id AND c.parent_id = t.id)`,
		StatusApproved, SystemActor, StatusApproved, SystemActor, StatusFailed, goalID).
		Scan(&g.TotalLeaves, &g.Approved, &g.AutoApproved, &g.Failed)
	if err != nil {
		return g, fmt.Errorf("failed to score goal: %w", err)
	}
	g.Unfinished = g.TotalLeaves - g.Approved - g.AutoApproved - g.Failed
	if g.TotalLeaves > 0 {
		g.Score = float64(g.Approved) / float64(g.TotalLeaves)
	}
	return g, nil
}

// FinishGoal closes the episode. Its reward is the fraction of leaf tasks that were verified
// as approved (auto-approvals excluded).
func (s *Store) FinishGoal(goalID, finalOutput string) (GoalScore, error) {
	var g GoalScore
	err := s.withTx(func(tx *sql.Tx) error {
		var err error
		if g, err = scoreGoal(tx, goalID); err != nil {
			return err
		}
		status := "completed"
		if g.Approved+g.AutoApproved < g.TotalLeaves {
			status = "partial"
		}
		if _, err := tx.Exec(`UPDATE goals SET status = ?, score = ?, final = ?, ended_at = ? WHERE id = ?`,
			status, g.Score, finalOutput, now(), goalID); err != nil {
			return err
		}
		return emit(tx, goalID, System, RootTaskID, "goal_completed", &g.Score, map[string]any{
			"final": finalOutput, "status": status, "score": g,
		})
	})
	return g, err
}

// Task returns one task.
func (s *Store) Task(goalID, id string) (*Task, error) {
	var t *Task
	err := s.withTx(func(tx *sql.Tx) error {
		var err error
		t, err = getTask(tx, goalID, id)
		return err
	})
	return t, err
}

// Tasks returns every task of a goal in creation order.
func (s *Store) Tasks(goalID string) ([]Task, error) {
	rows, err := s.db.Query(`SELECT `+taskCols+` FROM tasks WHERE goal_id = ? ORDER BY rowid`, goalID)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}
	return scanTasks(rows)
}

// Claimable returns the IDs of open tasks the agent may claim.
func (s *Store) Claimable(goalID, agent string) ([]string, error) {
	var ids []string
	err := s.withTx(func(tx *sql.Tx) error {
		var err error
		ids, err = claimableIDs(tx, goalID, agent)
		return err
	})
	return ids, err
}

// RunEventCount is how many task-graph decisions of the given kinds a run made.
func (s *Store) RunEventCount(runID string, kinds ...string) (int, error) {
	q := `SELECT COUNT(*) FROM task_events WHERE run_id = ?`
	args := []any{runID}
	if len(kinds) > 0 {
		q += ` AND kind IN (?` + strings.Repeat(`, ?`, len(kinds)-1) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// Events returns the goal's CTDG edge stream in order.
func (s *Store) Events(goalID string) ([]Event, error) {
	rows, err := s.db.Query(`SELECT id, goal_id, ts, src, dst, kind, reward, payload, run_id FROM task_events WHERE goal_id = ? ORDER BY id`, goalID)
	if err != nil {
		return nil, fmt.Errorf("failed to list events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var reward sql.NullFloat64
		var payload string
		if err := rows.Scan(&e.ID, &e.GoalID, &e.TS, &e.Src, &e.Dst, &e.Kind, &reward, &payload, &e.RunID); err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		if reward.Valid {
			e.Reward = &reward.Float64
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ponytail: results are cut at this length in the rendered board to protect agent context windows;
// the full text stays in the DB.
const maxRenderedResult = 2000

// RenderTree renders the task tree as indented text for agents to read.
func (s *Store) RenderTree(goalID string) (string, error) {
	tasks, err := s.Tasks(goalID)
	if err != nil {
		return "", err
	}
	children := map[string][]Task{}
	for _, t := range tasks {
		children[t.ParentID] = append(children[t.ParentID], t)
	}
	var sb strings.Builder
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, t := range children[parent] {
			pad := strings.Repeat("  ", depth)
			fmt.Fprintf(&sb, "%s[%s] (%s) %s", pad, t.ID, t.Status, t.Title)
			if t.Assignee != "" {
				fmt.Fprintf(&sb, " {assigned: %s}", t.Assignee)
			}
			if t.Owner != "" {
				fmt.Fprintf(&sb, " {by: %s}", t.Owner)
			}
			sb.WriteString("\n")
			if t.Description != "" {
				fmt.Fprintf(&sb, "%s    description: %s\n", pad, t.Description)
			}
			if len(t.Checks) > 0 {
				var cs []string
				for _, c := range t.Checks {
					cs = append(cs, strings.TrimSpace(c.Kind+" "+strconv.Quote(c.Arg)))
				}
				fmt.Fprintf(&sb, "%s    acceptance checks: %s\n", pad, strings.Join(cs, "; "))
			}
			if t.Result != "" {
				r := t.Result
				if len(r) > maxRenderedResult {
					r = strings.ToValidUTF8(r[:maxRenderedResult], "") + "...(truncated)"
				}
				fmt.Fprintf(&sb, "%s    result: %s\n", pad, r)
			}
			if t.Feedback != "" {
				fmt.Fprintf(&sb, "%s    verifier feedback: %s\n", pad, t.Feedback)
			}
			walk(t.ID, depth+1)
		}
	}
	walk("", 0)
	return sb.String(), nil
}
