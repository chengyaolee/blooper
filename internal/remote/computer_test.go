package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chengyaolee/blooper/internal/llm"
)

func TestComputerArguments(t *testing.T) {
	for _, args := range []string{
		`{"action":"screenshot"}`, `{"action":"click","x":0,"y":1599}`,
		`{"action":"double_click","x":12,"y":34}`, `{"action":"right_click","x":12,"y":34}`,
		`{"action":"type","text":"Hello 世界 👋"}`, `{"action":"key","keys":["CMD","SHIFT","A"]}`,
		`{"action":"scroll","direction":"down","amount":200}`,
	} {
		got, err := ParseArg("computer", json.RawMessage(args))
		if err != nil || !json.Valid([]byte(got)) {
			t.Errorf("%s: %s %v", args, got, err)
		}
	}
	for _, args := range []string{
		`{}`, `null`, `{"action":"shell","text":"whoami"}`, `{"action":"screenshot","text":"ignored"}`,
		`{"action":"click","x":-1,"y":0}`, `{"action":"click","x":1600,"y":0}`, `{"action":"click","x":1.5,"y":0}`,
		`{"action":"click","x":1}`, `{"action":"type","text":""}`, `{"action":"type","text":null}`,
		`{"action":"key","keys":["CMD"]}`, `{"action":"key","keys":["CMD","CMD","A"]}`,
		`{"action":"key","keys":["LAUNCH_TERMINAL"]}`, `{"action":"scroll","direction":"down","amount":1001}`,
		`{"action":"screenshot"} {}`, `{"action":"screenshot","command":"open"}`,
	} {
		if _, err := ParseArg("computer", json.RawMessage(args)); err == nil {
			t.Errorf("accepted %s", args)
		}
	}
}

func testScreen(t *testing.T) *llm.Image {
	t.Helper()
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 10)), nil); err != nil {
		t.Fatal(err)
	}
	return &llm.Image{Data: base64.StdEncoding.EncodeToString(data.Bytes()), Width: 20, Height: 10}
}

func TestComputerApprovalAndEphemeralScreen(t *testing.T) {
	c, err := Open(Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.mu.Lock()
	defer c.mu.Unlock()
	d := &Device{ID: "mac", Name: "My Mac", Tools: []string{"computer"}, GrantExpires: time.Now().Add(time.Hour).Unix()}
	c.devices[d.ID] = d
	j := &Job{ID: "job", RunnerID: d.ID, State: StateRunning, lease: "lease", leaseExpires: time.Now().Add(time.Hour)}
	c.jobs[j.ID] = j
	tool := &remoteTool{c: c, j: j, runnerID: d.ID, name: "computer"}
	a, err := tool.propose(context.Background(), json.RawMessage(`{"action":"screenshot"}`), stepAssessment{})
	if err != nil || a.Status != ActionReady {
		t.Fatalf("screenshot under grant: %+v %v", a, err)
	}
	a.Status = ActionDispatched
	img := testScreen(t)
	if code, result := c.result(d, RunnerResult{JobID: j.ID, ActionID: a.ID, Lease: "stale", Status: ActionDone, Image: img}); code != 409 {
		t.Fatalf("stale result accepted: %d %v", code, result)
	}
	if code, result := c.result(d, RunnerResult{JobID: j.ID, ActionID: a.ID, Lease: j.lease, Status: ActionDone, Image: img}); code != 200 {
		t.Fatalf("screenshot result: %d %v", code, result)
	}
	if j.Screen == nil || a.Image == nil {
		t.Fatal("screen was not passed to planner and phone")
	}
	var persisted string
	if err := c.db.QueryRow(`SELECT data FROM records WHERE kind='job' AND id=?`, j.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, img.Data) || strings.Contains(persisted, `"screen"`) {
		t.Fatal("screenshot was persisted")
	}
	for _, args := range []string{`{"action":"click","x":5,"y":6}`, `{"action":"type","text":"hello"}`, `{"action":"key","keys":["ENTER"]}`, `{"action":"scroll","direction":"up","amount":50}`} {
		input, err := tool.propose(context.Background(), json.RawMessage(args), stepAssessment{Risk: "routine"})
		if err != nil || input.Status != ActionAwaiting || input.Challenge == "" {
			t.Fatalf("input bypassed approval: %+v %v", input, err)
		}
		if string(input.Args) != args {
			t.Fatalf("approval changed arguments: %s", input.Args)
		}
		r := httptest.NewRequest("POST", "/v1/jobs/job/approval", nil)
		r.SetPathValue("id", j.ID)
		body, _ := json.Marshal(map[string]any{"challenge": input.Challenge, "approve": true})
		if code, _ := c.handleApproval(r, nil, body); code != 200 {
			t.Fatal(code)
		}
		if code, _ := c.handleApproval(r, nil, body); code != 409 {
			t.Fatal("approval challenge reused")
		}
		input.Status = ActionDone
	}
	other := &Job{ID: "other", RunnerID: d.ID, State: StateRunning}
	c.jobs[other.ID] = other
	if _, err := (&remoteTool{c: c, j: other, runnerID: d.ID, name: "computer"}).propose(context.Background(), json.RawMessage(`{"action":"screenshot"}`), stepAssessment{}); err == nil {
		t.Fatal("concurrent GUI jobs allowed")
	}
	d.Apps = []string{"notes"}
	if _, err := (&remoteTool{c: c, j: other, runnerID: d.ID, name: "app_open"}).propose(context.Background(), json.RawMessage(`{"app":"notes"}`), stepAssessment{}); err == nil {
		t.Fatal("another job could change the active application during computer control")
	}
	d.Tools = nil
	if _, err := tool.propose(context.Background(), json.RawMessage(`{"action":"screenshot"}`), stepAssessment{}); err == nil {
		t.Fatal("disabled computer tool allowed")
	}
}

func TestScreenValidation(t *testing.T) {
	img := testScreen(t)
	if err := ValidateScreen(img); err != nil {
		t.Fatal(err)
	}
	img.Width++
	if err := ValidateScreen(img); err == nil {
		t.Fatal("false dimensions accepted")
	}
	img.Data = strings.Repeat("A", (700<<10)+1)
	if err := ValidateScreen(img); err == nil {
		t.Fatal("oversized screen accepted")
	}
	img.Data = "not an image"
	if err := ValidateScreen(img); err == nil {
		t.Fatal("invalid screen accepted")
	}
}
