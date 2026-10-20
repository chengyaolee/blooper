package memory

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func act(agent string) Actor { return Actor{Agent: agent, RunID: "run-" + agent} }

var boss = act("boss")

func TestTaskGraphLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	goal, err := store.CreateGoal("Write a market report", boss, nil, nil)
	if err != nil {
		t.Fatalf("create goal: %v", err)
	}

	ids, err := store.Decompose(goal, RootTaskID, boss, []SubtaskSpec{
		{Title: "Research competitors"},
		{Title: "Draft email", Assignee: "emailer"},
	})
	if err != nil || len(ids) != 2 || ids[0] != "1.1" || ids[1] != "1.2" {
		t.Fatalf("decompose: ids=%v err=%v", ids, err)
	}

	// Delegated task is reserved for its assignee.
	if err := store.Claim(goal, "1.2", act("researcher")); err == nil {
		t.Fatal("researcher should not be able to claim a task assigned to emailer")
	}
	if err := store.Claim(goal, "1.1", act("researcher")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.Claim(goal, "1.1", act("emailer")); err == nil {
		t.Fatal("double claim should fail")
	}

	// A worker can decompose its own claimed task one level deeper.
	if ids, err := store.Decompose(goal, "1.1", act("researcher"), []SubtaskSpec{{Title: "Find pricing"}}); err != nil || ids[0] != "1.1.1" {
		t.Fatalf("nested decompose: ids=%v err=%v", ids, err)
	}

	// Reject twice then approve: reopened tasks can be retried.
	for i := 0; i < 2; i++ {
		if err := store.Claim(goal, "1.1.1", act("researcher")); err != nil {
			t.Fatalf("claim 1.1.1: %v", err)
		}
		if err := store.Complete(goal, "1.1.1", act("researcher"), "guess"); err != nil {
			t.Fatalf("complete: %v", err)
		}
		if err := store.Verify(goal, "1.1.1", boss, false, "cite sources"); err != nil {
			t.Fatalf("reject: %v", err)
		}
	}
	_ = store.Claim(goal, "1.1.1", act("researcher"))
	_ = store.Complete(goal, "1.1.1", act("researcher"), "$10/mo per source X")
	if err := store.Verify(goal, "1.1.1", boss, true, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Third rejection fails the task.
	for i := 0; i < MaxAttempts; i++ {
		_ = store.Claim(goal, "1.2", act("emailer"))
		_ = store.Complete(goal, "1.2", act("emailer"), "draft")
		if err := store.Verify(goal, "1.2", boss, false, "too vague"); err != nil {
			t.Fatalf("reject 1.2: %v", err)
		}
	}

	tasks, _ := store.Tasks(goal)
	status := map[string]string{}
	for _, tk := range tasks {
		status[tk.ID] = tk.Status
	}
	want := map[string]string{"1": StatusDecomposed, "1.1": StatusApproved, "1.1.1": StatusApproved, "1.2": StatusFailed}
	for id, s := range want {
		if status[id] != s {
			t.Errorf("task %s: want %s, got %s", id, s, status[id])
		}
	}

	score, err := store.FinishGoal(goal, "report")
	if err != nil || score.Score != 0.5 || score.Failed != 1 {
		t.Fatalf("finish: score=%+v err=%v (want 0.5: 1 of 2 leaves approved, 1 failed)", score, err)
	}
	if report, _ := store.VerifyTrace(goal); !report.OK {
		t.Errorf("trace should verify: %+v", report)
	}

	events, _ := store.Events(goal)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
		if e.Kind == "claimed" && e.Dst == "1.1" {
			var p struct{ Options []string }
			_ = json.Unmarshal(e.Payload, &p)
			if strings.Join(p.Options, ",") != "1.1" {
				t.Errorf("claim options for researcher: want [1.1], got %v", p.Options)
			}
		}
		if e.Kind == "verified" && e.Reward == nil {
			t.Error("verified event missing reward")
		}
	}
	got := strings.Join(kinds, " ")
	for _, k := range []string{"goal_created", "decomposed", "assigned", "claimed", "completed", "verified", "reopened", "rolled_up", "failed", "goal_completed"} {
		if !strings.Contains(got, k) {
			t.Errorf("missing %s event in %s", k, got)
		}
	}

	tree, _ := store.RenderTree(goal)
	if !strings.Contains(tree, "  [1.1] (approved)") || !strings.Contains(tree, "{assigned: emailer}") {
		t.Errorf("unexpected tree:\n%s", tree)
	}
}

func TestRollUpApprovesRoot(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	goal, _ := store.CreateGoal("g", boss, nil, nil)
	_, _ = store.Decompose(goal, RootTaskID, boss, []SubtaskSpec{{Title: "a"}})
	_ = store.Claim(goal, "1.1", act("w"))
	_ = store.Complete(goal, "1.1", act("w"), "ok")
	_ = store.Verify(goal, "1.1", boss, true, "")

	tasks, _ := store.Tasks(goal)
	if tasks[0].Status != StatusApproved {
		t.Fatalf("root should roll up to approved, got %s", tasks[0].Status)
	}
}
