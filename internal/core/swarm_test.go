package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/chengyaolee/blooper/internal/agent"
	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/memory"
	"github.com/chengyaolee/blooper/internal/trace"
)

// scriptedEngine plays every role in the swarm by reading the prompt and how many
// tool results have come back since it.
type scriptedEngine struct {
	verifierDown bool // controller verification calls fail, as if the model were offline
}

var verifyIDRe = regexp.MustCompile(`task_id "([0-9.]+)"`)

func call(name string, args map[string]any) *llm.ChatResponse {
	b, _ := json.Marshal(args)
	return &llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "c", Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: string(b)}},
	}}}
}

func reply(s string) *llm.ChatResponse {
	return &llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: s}}
}

func (e scriptedEngine) Generate(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	prompt, step := "", 0
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			prompt, step = m.Content, 0
		case "tool":
			step++
		}
	}

	switch {
	case strings.Contains(prompt, "You are the controller"):
		if step == 0 {
			return call("taskboard", map[string]any{"action": "decompose", "task_id": "1", "subtasks": []map[string]any{
				{"title": "research", "checks": []map[string]string{{"kind": "contains", "arg": "result"}}},
				// The writer's first draft fails this check and is sent back without asking the controller.
				{"title": "write", "assignee": "writer", "checks": []map[string]string{{"kind": "contains", "arg": "fixed"}}},
			}}), nil
		}
	case strings.Contains(prompt, "Tasks you can claim:"):
		line := strings.SplitN(strings.SplitN(prompt, "Tasks you can claim: ", 2)[1], "\n", 2)[0]
		id := strings.Split(line, ", ")[0]
		result := "result for " + id
		if strings.Contains(prompt, "verifier feedback") {
			result += " (fixed)"
		}
		switch step {
		case 0:
			return call("taskboard", map[string]any{"action": "claim", "task_id": id}), nil
		case 1:
			return call("taskboard", map[string]any{"action": "complete", "task_id": id, "result": result}), nil
		}
	case strings.Contains(prompt, "You are verifying"):
		if e.verifierDown {
			return nil, errors.New("model offline")
		}
		if step == 0 {
			id := verifyIDRe.FindStringSubmatch(prompt)[1]
			return call("taskboard", map[string]any{"action": "verify", "task_id": id, "approved": true}), nil
		}
	case strings.Contains(prompt, "Write the final answer"):
		return reply("FINAL ANSWER"), nil
	}
	return reply("ok"), nil
}

func (scriptedEngine) Stream(ctx context.Context, req *llm.ChatRequest, out chan<- llm.StreamChunk) error {
	close(out)
	return nil
}
func (scriptedEngine) KVStats() (*llm.KVMetrics, error)                 { return &llm.KVMetrics{}, nil }
func (scriptedEngine) ListModels(ctx context.Context) ([]string, error) { return nil, nil }

func newTestSwarm(t *testing.T, engine scriptedEngine) (*Swarm, *memory.Store) {
	t.Helper()
	store, err := memory.Open(filepath.Join(t.TempDir(), "swarm.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	reg := agent.NewAgentRegistry()
	for _, name := range []string{"boss", "researcher", "writer"} {
		reg.Register(&agent.AgentConfig{
			Name:    name,
			Persona: agent.PersonaConfig{Role: name, SystemPrompt: "You are " + name},
			Model:   agent.ModelConfig{Preferred: "fake"},
		})
	}
	s := NewSwarm(store, reg, trace.Engine(engine), nil, nil, "boss", 2)
	s.OutputDir = t.TempDir()
	return s, store
}

func goalIDFrom(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`goal ([0-9a-f-]{36})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no goal id in output: %s", out)
	}
	return m[1]
}

func TestSwarmRunsGoalEndToEnd(t *testing.T) {
	swarm, store := newTestSwarm(t, scriptedEngine{})

	// Mirrors production: a chat turn is the root run that spawns the goal.
	ctx, chat := trace.Start(context.Background(), store, trace.Info{Agent: "boss", Role: "chat", Input: "ship a report"})
	out, err := swarm.RunGoal(ctx, "ship a report", "", []string{"researcher", "writer", "writer"}) // duplicate is ignored
	chat.End(out, err)
	if err != nil {
		t.Fatalf("run goal: %v", err)
	}
	if !strings.Contains(out, "FINAL ANSWER") || !strings.Contains(out, "2/2 subtasks verified") || !strings.Contains(out, "protocol trace verified") {
		t.Fatalf("unexpected output: %s", out)
	}

	goalID := goalIDFrom(t, out)
	tasks, _ := store.Tasks(goalID)
	if tasks[0].Status != memory.StatusApproved {
		t.Errorf("root should be approved, got %s", tasks[0].Status)
	}
	for _, tk := range tasks {
		if tk.ID == "1.2" && (tk.Owner != "writer" || tk.Attempts != 1 || tk.VerifiedBy != "boss") {
			t.Errorf("1.2 should be redone by writer after a failed check, then approved by boss: %+v", tk)
		}
	}

	events, _ := store.Events(goalID)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Src+":"+e.Kind)
		if e.Src != memory.SystemActor && e.RunID == "" {
			t.Errorf("agent event %s by %s is not linked to a run", e.Kind, e.Src)
		}
	}
	got := strings.Join(kinds, " ")
	for _, want := range []string{"boss:goal_created", "boss:decomposed", "boss:assigned", "writer:claimed",
		"writer:completed", "system:checked", "system:verified", "system:reopened", "boss:verified",
		"system:rolled_up", "system:goal_completed"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in event stream: %s", want, got)
		}
	}

	// The whole episode exports as one trajectory: chat -> goal -> plan/work/verify/synthesize.
	var buf bytes.Buffer
	if n, err := store.ExportJSONL(&buf); err != nil || n != 1 {
		t.Fatalf("export: n=%d err=%v", n, err)
	}
	var ep struct {
		Runs []struct {
			Role  string `json:"role"`
			Steps []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"steps"`
		} `json:"runs"`
		Goals []struct {
			Verification struct {
				OK bool `json:"ok"`
			} `json:"verification"`
			Events []any `json:"events"`
		} `json:"goals"`
	}
	if err := json.Unmarshal(buf.Bytes(), &ep); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	roles, stepKinds := map[string]int{}, map[string]int{}
	for _, r := range ep.Runs {
		roles[r.Role]++
		for _, s := range r.Steps {
			stepKinds[s.Kind+":"+s.Name]++
		}
	}
	for _, role := range []string{"chat", "goal", "plan", "work", "verify", "synthesize"} {
		if roles[role] == 0 {
			t.Errorf("no %s run in export: %v", role, roles)
		}
	}
	if stepKinds["llm:fake"] == 0 || stepKinds["tool:taskboard"] == 0 {
		t.Errorf("LLM and tool steps should be recorded: %v", stepKinds)
	}
	if len(ep.Goals) != 1 || !ep.Goals[0].Verification.OK || len(ep.Goals[0].Events) != len(events) {
		t.Errorf("goal with verified trace and all events should be exported: %+v", ep.Goals)
	}
}

func TestSwarmNeverApprovesWhenVerifierFails(t *testing.T) {
	swarm, store := newTestSwarm(t, scriptedEngine{verifierDown: true})

	out, err := swarm.RunGoal(context.Background(), "ship a report", "", []string{"researcher", "writer"})
	if err != nil {
		t.Fatalf("run goal: %v", err)
	}
	goalID := goalIDFrom(t, out)

	// Both results pass their checks (the writer's after one check rejection), but the
	// controller can never judge them, so nothing may count as verified.
	if !strings.Contains(out, "0/2 subtasks verified, 0 auto-approved") || !strings.Contains(out, "liveness") {
		t.Fatalf("an offline verifier must leave work unverified and flag liveness: %s", out)
	}
	events, _ := store.Events(goalID)
	errs := 0
	for _, e := range events {
		if e.Kind == "verification_error" {
			errs++
		}
		if e.Kind == "verified" && e.Src == memory.SystemActor && e.Reward != nil && *e.Reward > 0 {
			t.Errorf("auto-approval after a verifier error: %+v", e)
		}
	}
	if errs == 0 {
		t.Error("verification errors should be logged")
	}
	report, _ := store.VerifyTrace(goalID)
	if len(report.Safety) != 0 || len(report.Projection) != 0 || len(report.Liveness) != 2 {
		t.Errorf("want a safe trace with 2 unfinished tasks, got %+v", report)
	}
}

func TestRunChecks(t *testing.T) {
	dir := t.TempDir()
	results := RunChecks([]memory.Check{
		{Kind: "contains", Arg: "HTTPS://"},
		{Kind: "regex", Arg: `\d{4}`},
		{Kind: "min_length", Arg: "100"},
		{Kind: "max_length", Arg: "100"},
		{Kind: "json"},
		{Kind: "file_exists", Arg: "../../etc/passwd"},
		{Kind: "file_exists", Arg: "report.md"},
	}, "see https://x.io in 2024", dir)
	want := []bool{true, true, false, true, false, false, false}
	for i, r := range results {
		if r.Passed != want[i] {
			t.Errorf("check %d (%s %q): passed=%v, want %v (%s)", i, r.Kind, r.Arg, r.Passed, want[i], r.Detail)
		}
	}
}
