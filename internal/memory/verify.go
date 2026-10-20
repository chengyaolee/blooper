package memory

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Formal trace verification.
//
// The task-graph protocol is a state machine per task:
//
//	open --claimed--> claimed --completed--> done --verified(+)--> approved
//	claimed --abandoned--> open                done --verified(-)--> reopened -> open | failed
//	open|claimed(by src) --decomposed--> decomposed --rolled_up(all children approved)--> approved
//
// CheckTrace replays a goal's event log from scratch against that machine and reports:
//   - safety:     an event that was not a legal transition, or a state invariant that broke
//                 (double ownership, approval without approved children, wrong rewards, ...)
//   - liveness:   once the goal completed, every task must be terminal (approved / failed /
//                 decomposed into terminal children) - nothing left open, claimed or unverified
//   - projection: the replayed state must equal the materialized tasks table, proving the
//                 event log is a faithful source of truth for training data
//
// A trace that passes is a valid execution of the protocol; one that fails is flagged on the
// goal so it can be excluded from RL data and debugged.

// Violation is one broken rule.
type Violation struct {
	EventID int64  `json:"event_id,omitempty"`
	Task    string `json:"task,omitempty"`
	Rule    string `json:"rule"`
	Detail  string `json:"detail"`
}

// TraceReport is the result of verifying one goal's trace.
type TraceReport struct {
	OK         bool        `json:"ok"`
	Events     int         `json:"events"`
	Finished   bool        `json:"finished"`
	Safety     []Violation `json:"safety,omitempty"`
	Liveness   []Violation `json:"liveness,omitempty"`
	Projection []Violation `json:"projection,omitempty"`
}

const statusRejecting = "rejecting" // transient: verified(-) seen, reopened/failed must follow

type replayNode struct {
	parent, status, owner, assignee, verifiedBy string
	attempts                                    int
	children                                    []string
}

type replay struct {
	nodes      map[string]*replayNode
	controller string
	report     *TraceReport
	eventID    int64
}

func (r *replay) fail(task, rule, format string, args ...any) {
	r.report.Safety = append(r.report.Safety, Violation{EventID: r.eventID, Task: task, Rule: rule, Detail: fmt.Sprintf(format, args...)})
}

func (r *replay) claimable(agent string) []string {
	var ids []string
	for id, n := range r.nodes {
		if n.status == StatusOpen && (n.assignee == "" || n.assignee == agent) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func (r *replay) score() float64 {
	leaves, approved := 0, 0
	for _, n := range r.nodes {
		if len(n.children) > 0 {
			continue
		}
		leaves++
		if n.status == StatusApproved && n.verifiedBy != SystemActor {
			approved++
		}
	}
	if leaves == 0 {
		return 0
	}
	return float64(approved) / float64(leaves)
}

// VerifyTrace loads a goal's events and tasks and checks them.
func (s *Store) VerifyTrace(goalID string) (*TraceReport, error) {
	events, err := s.Events(goalID)
	if err != nil {
		return nil, err
	}
	tasks, err := s.Tasks(goalID)
	if err != nil {
		return nil, err
	}
	return CheckTrace(events, tasks), nil
}

// CheckTrace replays events (in id order) against the protocol and compares the outcome to tasks.
func CheckTrace(events []Event, tasks []Task) *TraceReport {
	r := &replay{nodes: map[string]*replayNode{}, report: &TraceReport{Events: len(events)}}
	var lastTS int64
	batchParent, batchSrc := "", "" // decomposition in progress (one decomposed event per child)

	for i, e := range events {
		r.eventID = e.ID
		var p map[string]any
		_ = json.Unmarshal(e.Payload, &p)
		n := r.nodes[e.Dst]

		if e.TS <= lastTS {
			r.fail(e.Dst, "time_monotonic", "ts %d not after previous %d", e.TS, lastTS)
		}
		lastTS = e.TS
		if r.report.Finished {
			r.fail(e.Dst, "after_completion", "%s event after goal_completed", e.Kind)
		}
		if i == 0 && e.Kind != "goal_created" {
			r.fail(e.Dst, "starts_with_goal", "first event is %s", e.Kind)
		}
		if e.Reward != nil && e.Kind != "verified" && e.Kind != "goal_completed" {
			r.fail(e.Dst, "reward_placement", "%s event carries a reward", e.Kind)
		}
		if e.Kind != "decomposed" && e.Kind != "assigned" {
			batchParent, batchSrc = "", ""
		}
		if n == nil && e.Kind != "goal_created" && e.Kind != "decomposed" {
			r.fail(e.Dst, "unknown_task", "%s on a task that was never created", e.Kind)
			continue
		}

		switch e.Kind {
		case "goal_created":
			if i != 0 || e.Dst != RootTaskID {
				r.fail(e.Dst, "goal_created", "goal_created must be the first event and target the root")
			}
			r.controller = e.Src
			r.nodes[RootTaskID] = &replayNode{status: StatusOpen}

		case "decomposed":
			parentID, _ := p["parent"].(string)
			parent := r.nodes[parentID]
			switch {
			case parent == nil:
				r.fail(e.Dst, "decompose_parent", "parent %q does not exist", parentID)
				continue
			case n != nil:
				r.fail(e.Dst, "decompose_unique", "task already exists")
				continue
			case !strings.HasPrefix(e.Dst, parentID+"."):
				r.fail(e.Dst, "decompose_id", "child id is not under parent %s", parentID)
			}
			sameBatch := parent.status == StatusDecomposed && batchParent == parentID && batchSrc == e.Src
			if !sameBatch && parent.status != StatusOpen && !(parent.status == StatusClaimed && parent.owner == e.Src) {
				r.fail(parentID, "decompose_precondition", "%s decomposed a task that is %s (owner %q)", e.Src, parent.status, parent.owner)
			}
			parent.status = StatusDecomposed
			parent.children = append(parent.children, e.Dst)
			r.nodes[e.Dst] = &replayNode{parent: parentID, status: StatusOpen}
			batchParent, batchSrc = parentID, e.Src

		case "assigned":
			if i == 0 || events[i-1].Kind != "decomposed" || events[i-1].Dst != e.Dst {
				r.fail(e.Dst, "assign_with_decompose", "assignment must immediately follow the task's creation")
			}
			n.assignee, _ = p["assignee"].(string)

		case "claimed":
			want := r.claimable(e.Src)
			if n.status != StatusOpen || (n.assignee != "" && n.assignee != e.Src) {
				r.fail(e.Dst, "claim_precondition", "%s claimed a task that is %s (assignee %q)", e.Src, n.status, n.assignee)
			}
			var got []string
			for _, o := range asSlice(p["options"]) {
				if s, ok := o.(string); ok {
					got = append(got, s)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				r.fail(e.Dst, "claim_options", "logged action space %v, replayed %v", got, want)
			}
			n.status, n.owner = StatusClaimed, e.Src

		case "completed":
			if n.status != StatusClaimed || n.owner != e.Src {
				r.fail(e.Dst, "complete_precondition", "%s completed a task that is %s (owner %q)", e.Src, n.status, n.owner)
			}
			n.status = StatusDone

		case "checked":
			if n.status != StatusDone || e.Src != SystemActor {
				r.fail(e.Dst, "check_precondition", "checks by %s on a task that is %s", e.Src, n.status)
			}

		case "verification_error":
			if n.status != StatusDone {
				r.fail(e.Dst, "verify_error_precondition", "verification error on a task that is %s", n.status)
			}

		case "verified":
			if n.status != StatusDone {
				r.fail(e.Dst, "verify_precondition", "verified a task that is %s", n.status)
			}
			if e.Src != r.controller && e.Src != SystemActor {
				r.fail(e.Dst, "verify_authority", "%s is not the controller (%s)", e.Src, r.controller)
			}
			verdict, _ := p["verdict"].(string)
			if worker, _ := p["worker"].(string); worker != n.owner {
				r.fail(e.Dst, "verify_worker", "payload worker %q, owner %q", worker, n.owner)
			}
			wantReward := map[string]float64{"approved": 1, "rejected": -1}[verdict]
			if e.Reward == nil || *e.Reward != wantReward || wantReward == 0 {
				r.fail(e.Dst, "verify_reward", "verdict %q with reward %v", verdict, e.Reward)
			}
			n.verifiedBy = e.Src
			if verdict == "approved" {
				n.status = StatusApproved
			} else {
				n.status = statusRejecting
				n.attempts++
			}

		case "reopened":
			if n.status != statusRejecting || n.attempts >= MaxAttempts || i == 0 || events[i-1].Dst != e.Dst || events[i-1].Kind != "verified" {
				r.fail(e.Dst, "reopen_precondition", "reopened while %s with %d attempts", n.status, n.attempts)
			}
			n.status, n.owner = StatusOpen, ""

		case "failed":
			if n.status != statusRejecting || n.attempts < MaxAttempts || e.Src != SystemActor {
				r.fail(e.Dst, "fail_precondition", "failed while %s with %d attempts", n.status, n.attempts)
			}
			n.status = StatusFailed

		case "rolled_up":
			if n.status != StatusDecomposed || !r.allApproved(n.children) {
				r.fail(e.Dst, "rollup_precondition", "rolled up a %s task whose children are not all approved", n.status)
			}
			n.status, n.verifiedBy = StatusApproved, SystemActor

		case "abandoned":
			if n.status != StatusClaimed || n.owner != e.Src {
				r.fail(e.Dst, "abandon_precondition", "%s abandoned a task that is %s (owner %q)", e.Src, n.status, n.owner)
			}
			n.status, n.owner = StatusOpen, ""

		case "goal_completed":
			r.report.Finished = true
			if want := r.score(); e.Reward == nil || math.Abs(*e.Reward-want) > 1e-9 {
				r.fail(e.Dst, "episode_reward", "logged reward %v, replayed score %v", e.Reward, want)
			}

		default:
			r.fail(e.Dst, "unknown_kind", "unknown event kind %q", e.Kind)
		}

		// Rejection must be resolved by the very next event.
		if i > 0 && events[i-1].Kind == "verified" {
			if prev := r.nodes[events[i-1].Dst]; prev != nil && prev.status == statusRejecting {
				r.fail(events[i-1].Dst, "reject_resolution", "rejection not followed by reopened/failed")
				prev.status = StatusOpen
			}
		}
	}

	r.eventID = 0
	for id, n := range r.nodes {
		switch {
		case n.status == StatusApproved && len(n.children) > 0 && !r.allApproved(n.children):
			r.fail(id, "approval_requires_children", "approved but not all children approved")
		case n.status == StatusDecomposed && r.allApproved(n.children):
			r.fail(id, "missing_rollup", "all children approved but task never rolled up")
		case n.status == statusRejecting:
			r.fail(id, "reject_resolution", "rejection never resolved")
		}
		if r.report.Finished && (n.status == StatusOpen || n.status == StatusClaimed || n.status == StatusDone) {
			r.report.Liveness = append(r.report.Liveness, Violation{Task: id, Rule: "terminates", Detail: "goal completed with task " + n.status})
		}
	}

	r.compare(tasks)
	sortViolations(r.report.Liveness)
	r.report.OK = len(r.report.Safety) == 0 && len(r.report.Liveness) == 0 && len(r.report.Projection) == 0
	return r.report
}

func (r *replay) allApproved(ids []string) bool {
	for _, id := range ids {
		if c := r.nodes[id]; c == nil || c.status != StatusApproved {
			return false
		}
	}
	return true
}

// compare checks the replayed state against the materialized tasks table.
func (r *replay) compare(tasks []Task) {
	add := func(task, detail string) {
		r.report.Projection = append(r.report.Projection, Violation{Task: task, Rule: "projection", Detail: detail})
	}
	seen := map[string]bool{}
	for _, t := range tasks {
		seen[t.ID] = true
		n := r.nodes[t.ID]
		if n == nil {
			add(t.ID, "task exists in table but not in event log")
			continue
		}
		for _, f := range []struct{ name, table, replayed string }{
			{"status", t.Status, n.status},
			{"owner", t.Owner, n.owner},
			{"assignee", t.Assignee, n.assignee},
			{"parent", t.ParentID, n.parent},
			{"verified_by", t.VerifiedBy, n.verifiedBy},
			{"attempts", fmt.Sprint(t.Attempts), fmt.Sprint(n.attempts)},
		} {
			if f.table != f.replayed {
				add(t.ID, fmt.Sprintf("%s: table %q, replayed %q", f.name, f.table, f.replayed))
			}
		}
	}
	for id := range r.nodes {
		if !seen[id] {
			add(id, "task in event log but missing from table")
		}
	}
	sortViolations(r.report.Projection)
}

func sortViolations(v []Violation) {
	slices.SortFunc(v, func(a, b Violation) int { return strings.Compare(a.Task+a.Detail, b.Task+b.Detail) })
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
