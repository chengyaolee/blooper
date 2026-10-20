package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chengyaolee/blooper/internal/memory"
)

func TestTaskBoardGuards(t *testing.T) {
	store, err := memory.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	workers := []string{"researcher", "writer"}
	goal, _ := store.CreateGoal("g", memory.Actor{Agent: "boss"}, workers, nil)
	boss := NewTaskBoardTool(store, goal, memory.Actor{Agent: "boss"}, true, workers)
	writer := NewTaskBoardTool(store, goal, memory.Actor{Agent: "writer"}, false, workers)

	exec := func(tool *TaskBoardTool, args string) *ToolResult {
		res, err := tool.Execute(context.Background(), []byte(args))
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		return res
	}

	for _, c := range []struct {
		name, args, wantErr string
		tool                *TaskBoardTool
	}{
		{"unknown assignee", `{"action":"decompose","task_id":"1","subtasks":[{"title":"x","assignee":"reseacher"}]}`, "unknown assignee", boss},
		{"bad check", `{"action":"decompose","task_id":"1","subtasks":[{"title":"x","checks":[{"kind":"vibes"}]}]}`, "unknown check kind", boss},
		{"worker splits unclaimed task", `{"action":"decompose","task_id":"1","subtasks":[{"title":"x"}]}`, "claim task 1", writer},
	} {
		if res := exec(c.tool, c.args); !strings.Contains(res.Error, c.wantErr) {
			t.Errorf("%s: want error %q, got %+v", c.name, c.wantErr, res)
		}
	}

	if res := exec(boss, `{"action":"decompose","task_id":"1","subtasks":[{"title":"x","assignee":"writer","checks":[{"kind":"min_length","arg":"3"}]}]}`); res.Error != "" {
		t.Fatalf("decompose: %s", res.Error)
	}
	if res := exec(boss, `{"action":"claim","task_id":"1.1"}`); !strings.Contains(res.Error, "controller") {
		t.Errorf("controller must not claim: %+v", res)
	}
	exec(writer, `{"action":"claim","task_id":"1.1"}`)
	exec(writer, `{"action":"complete","task_id":"1.1","result":"done"}`)

	if res := exec(boss, `{"action":"verify","task_id":"1.1","feedback":"looks good"}`); !strings.Contains(res.Error, "approved") {
		t.Errorf("verify without a verdict must not count as a rejection: %+v", res)
	}
	if res := exec(boss, `{"action":"verify","task_id":"1.1","approved":false}`); !strings.Contains(res.Error, "feedback") {
		t.Errorf("rejection without feedback should be refused: %+v", res)
	}
	if res := exec(writer, `{"action":"verify","task_id":"1.1","approved":true}`); !strings.Contains(res.Error, "only the controller") {
		t.Errorf("worker must not verify: %+v", res)
	}
	if res := exec(boss, `{"action":"verify","task_id":"1.1","approved":true}`); res.Error != "" {
		t.Fatalf("verify: %s", res.Error)
	}
	if tk, _ := store.Task(goal, "1.1"); tk.Status != memory.StatusApproved || tk.Attempts != 0 {
		t.Errorf("refused verify calls must not consume attempts: %+v", tk)
	}
	if view := exec(writer, `{"action":"view"}`).Output; !strings.Contains(view, `acceptance checks: min_length "3"`) {
		t.Errorf("board should show acceptance checks:\n%s", view)
	}
}
