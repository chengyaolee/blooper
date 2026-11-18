package remote

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chengyaolee/blooper/internal/llm"
)

type reviewEngine struct {
	response string
	err      error
	requests []*llm.ChatRequest
}

func (e *reviewEngine) Generate(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	e.requests = append(e.requests, req)
	return &llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: e.response}}, e.err
}
func (*reviewEngine) Stream(context.Context, *llm.ChatRequest, chan<- llm.StreamChunk) error {
	return nil
}
func (*reviewEngine) KVStats() (*llm.KVMetrics, error)             { return nil, nil }
func (*reviewEngine) ListModels(context.Context) ([]string, error) { return nil, nil }

func TestRoutineAutoApproval(t *testing.T) {
	engine := &reviewEngine{response: `{"risk":"routine","reason":"Focus the note's empty draft field."}`}
	c, err := Open(Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	d := &Device{ID: "mac", Name: "Mac", AutoApproveRoutine: true, Tools: []string{"computer", "app_open", "web_search"}, Apps: []string{"notes"}, GrantExpires: time.Now().Add(time.Hour).Unix()}
	j := &Job{ID: "job", RunnerID: d.ID, State: StateRunning, Screen: testScreen(t), Transcript: "Write a draft note"}
	c.mu.Lock()
	c.devices[d.ID], c.jobs[j.ID] = d, j
	c.mu.Unlock()
	tool := &remoteTool{c: c, j: j, runnerID: d.ID, name: "computer"}
	click := json.RawMessage(`{"action":"click","x":5,"y":6}`)
	assessment := tool.assess(context.Background(), click)
	if assessment.Risk != "routine" || len(engine.requests) != 1 {
		t.Fatalf("screen review failed: %+v", assessment)
	}
	request := engine.requests[0]
	if len(request.Tools) != 0 || len(request.Messages[1].Images) != 1 || !strings.Contains(request.Messages[1].Content, "Write a draft note") || !strings.Contains(request.Messages[0].Content, "Critical:") {
		t.Fatal("review did not independently inspect the screen, task, and action without tools")
	}
	for _, test := range []struct {
		name, risk       string
		enabled, expired bool
		want             string
	}{
		{"routine", "routine", true, false, ActionReady},
		{"critical", "critical", true, false, ActionAwaiting},
		{"uncertain", "unknown", true, false, ActionAwaiting},
		{"missing review", "", true, false, ActionAwaiting},
		{"opt out", "routine", false, false, ActionAwaiting},
		{"expired grant", "routine", true, true, ActionAwaiting},
	} {
		t.Run(test.name, func(t *testing.T) {
			c.mu.Lock()
			defer c.mu.Unlock()
			d.AutoApproveRoutine = test.enabled
			d.GrantExpires = time.Now().Add(time.Hour).Unix()
			if test.expired {
				d.GrantExpires = 1
			}
			action, err := tool.propose(context.Background(), click, stepAssessment{Risk: test.risk, Reason: "Reviewed effect"})
			if err != nil || action.Status != test.want {
				t.Fatalf("%+v %v", action, err)
			}
			if test.want == ActionReady && (action.Challenge != "" || !strings.Contains(j.Events[len(j.Events)-1].Message, "Auto-approved routine step")) {
				t.Fatal("auto-approval not journaled")
			}
			if test.want == ActionAwaiting && action.Challenge == "" {
				t.Fatal("manual approval has no challenge")
			}
		})
	}
	c.mu.Lock()
	d.AutoApproveRoutine, d.GrantExpires = true, time.Now().Add(time.Hour).Unix()
	c.mu.Unlock()
	for _, response := range []string{"", "not JSON", `{"risk":"routine"}`, `{"risk":"safe","reason":"ok"}`, `{"risk":"critical","reason":"This sends a message."}`, `{"risk":"unknown","reason":"Target unclear."}`} {
		engine.response = response
		if got := tool.assess(context.Background(), click); got.Risk == "routine" {
			t.Fatalf("unsafe review auto-approved: %s", response)
		}
	}
	engine.response, engine.err = `{"risk":"routine","reason":"ok"}`, errors.New("provider unavailable")
	if got := tool.assess(context.Background(), click); got.Risk == "routine" {
		t.Fatal("provider failure auto-approved")
	}
	engine.err = nil
	count := len(engine.requests)
	for _, args := range []string{`{"action":"key","keys":["ENTER"]}`, `{"action":"key","keys":["CMD","S"]}`, `{"action":"key","keys":["BACKSPACE"]}`, `{"action":"type","text":"text\n"}`} {
		if got := tool.assess(context.Background(), json.RawMessage(args)); got.Risk != "critical" {
			t.Fatalf("potential commit input auto-approved: %s", args)
		}
	}
	if len(engine.requests) != count {
		t.Fatal("commit keys should require approval independently of AI review")
	}
	if got := tool.assess(context.Background(), json.RawMessage(`{"action":"scroll","direction":"down","amount":50}`)); got.Risk != "routine" || len(engine.requests) != count {
		t.Fatal("scrolling should not need AI review")
	}
	c.mu.Lock()
	j.Screen = nil
	c.mu.Unlock()
	if got := tool.assess(context.Background(), click); got.Risk == "routine" {
		t.Fatal("missing screen auto-approved")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	app := &remoteTool{c: c, j: j, runnerID: d.ID, name: "app_open"}
	if a, err := app.propose(context.Background(), json.RawMessage(`{"app":"notes"}`), stepAssessment{}); err != nil || a.Status != ActionReady {
		t.Fatalf("allowed app: %+v %v", a, err)
	}
	if _, err := app.propose(context.Background(), json.RawMessage(`{"app":"unlisted"}`), stepAssessment{}); err == nil {
		t.Fatal("auto-approval bypassed app allowlist")
	}
	search := &remoteTool{c: c, j: j, runnerID: d.ID, name: "web_search"}
	if a, err := search.propose(context.Background(), json.RawMessage(`{"query":"private task"}`), stepAssessment{Risk: "routine"}); err != nil || a.Status != ActionAwaiting {
		t.Fatal("web-search confirmation was bypassed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.propose(ctx, click, stepAssessment{Risk: "routine"}); err == nil {
		t.Fatal("auto-approval bypassed cancellation")
	}
}
