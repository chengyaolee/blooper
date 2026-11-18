package remote_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/remote"
	"github.com/chengyaolee/blooper/internal/runner"
)

// scriptedPlanner stands in for the model: one tool call chosen by keyword,
// then a final answer that repeats the tool result.
type scriptedPlanner struct{}

func (scriptedPlanner) Generate(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	var user, toolOut string
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			user = m.Content
		case "tool":
			toolOut = m.Content
		}
	}
	if toolOut != "" {
		return &llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: "Result: " + toolOut}}, nil
	}
	name, args := "repo_run_tests", `{"workspace":"demo"}`
	switch {
	case strings.Contains(user, "slow"):
		args = `{"workspace":"slow"}`
	case strings.Contains(user, "open"):
		name, args = "app_open", `{"app":"hello"}`
	case strings.Contains(user, "secrets"):
		args = `{"workspace":"/etc"}` // not an enrolled workspace
	}
	return &llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: name, Arguments: args}},
	}}}, nil
}
func (scriptedPlanner) Stream(context.Context, *llm.ChatRequest, chan<- llm.StreamChunk) error {
	return nil
}
func (scriptedPlanner) KVStats() (*llm.KVMetrics, error)             { return nil, nil }
func (scriptedPlanner) ListModels(context.Context) ([]string, error) { return nil, nil }

type client struct {
	t    *testing.T
	base string
	priv ed25519.PrivateKey
}

func (c client) do(method, uri string, in, out any) int {
	c.t.Helper()
	var body []byte
	if raw, ok := in.([]byte); ok {
		body = raw
	} else if in != nil {
		body, _ = json.Marshal(in)
	}
	req, err := remote.SignedRequest(method, c.base, uri, body, c.priv)
	if err != nil {
		c.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, uri, err, data)
		}
	}
	return resp.StatusCode
}

// waitJob follows the event feed until cond holds.
func (c client) waitJob(id string, what string, cond func(remote.Job) bool) remote.Job {
	c.t.Helper()
	var j remote.Job
	seen := 0
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if code := c.do("GET", fmt.Sprintf("/v1/jobs/%s?after=%d", id, seen), nil, &j); code != 200 {
			c.t.Fatalf("job feed: status %d", code)
		}
		for i, e := range j.Events {
			if e.Seq != i+1 {
				c.t.Fatalf("event seq not monotonic: %+v", j.Events)
			}
		}
		seen = len(j.Events)
		if cond(j) {
			return j
		}
	}
	c.t.Fatalf("timed out waiting for %s; job is %s (%s)", what, j.State, j.Detail)
	return j
}

func newKey(t *testing.T) ed25519.PrivateKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestRemoteEndToEnd(t *testing.T) {
	dir := t.TempDir()
	deepgram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token dg-key" || r.URL.Query().Get("model") != "nova-3" {
			http.Error(w, "bad request", 400)
			return
		}
		io.WriteString(w, `{"results":{"channels":[{"alternatives":[{"transcript":"run the tests"}]}]}}`)
	}))
	defer deepgram.Close()

	coord, err := remote.Open(remote.Config{
		DBPath: filepath.Join(dir, "remote.db"), Engine: scriptedPlanner{}, Model: "test",
		DeepgramKey: "dg-key", DeepgramURL: deepgram.URL,
		PollWait: 200 * time.Millisecond, LeaseTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer coord.Close()
	srv := httptest.NewServer(coord.Routes())
	defer srv.Close()

	// Pair the phone with the single-use code.
	phone := client{t, srv.URL, newKey(t)}
	pair := remote.PairRequest{Code: coord.NewPairCode(), Name: "Test Phone", PublicKey: phone.priv.Public().(ed25519.PublicKey)}
	if code := phone.do("POST", "/v1/pair", pair, nil); code != 201 {
		t.Fatalf("pair: status %d", code)
	}
	if code := (client{t, srv.URL, newKey(t)}).do("POST", "/v1/pair", pair, nil); code == 201 {
		t.Fatal("pairing code was accepted twice")
	}
	if code := (client{t, srv.URL, newKey(t)}).do("GET", "/v1/devices", nil, nil); code != 401 {
		t.Fatalf("unenrolled key: status %d, want 401", code)
	}

	// Enroll a runner: it proves its key, the phone reviews and approves.
	cfgPath := filepath.Join(dir, "runner.yaml")
	os.WriteFile(cfgPath, []byte(fmt.Sprintf(`
coordinator: %s
name: Test Desktop
workspaces:
  demo: {path: %q, test_cmd: [sh, -c, "echo tests ran; echo $SECRET_TOKEN; exit 3"]}
  slow: {path: %q, test_cmd: [sleep, "60"]}
apps:
  hello: ["true"]
`, srv.URL, dir, dir)), 0600)
	t.Setenv("SECRET_TOKEN", "leaked")
	r, err := runner.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := os.ReadFile(filepath.Join(dir, "runner.key"))
	rawSeed, _ := hex.DecodeString(string(seed))
	desktop := client{t, srv.URL, ed25519.NewKeyFromSeed(rawSeed)}
	enroll := remote.EnrollRequest{
		Name: "Test Desktop", Kind: remote.KindDesktop, OS: "test", PublicKey: desktop.priv.Public().(ed25519.PublicKey),
		Tools: []string{"repo_run_tests", "app_open"}, Workspaces: []string{"demo", "slow"}, Apps: []string{"hello"},
	}
	var ch remote.EnrollChallenge
	if code := desktop.do("POST", "/v1/enroll", enroll, &ch); code != 201 {
		t.Fatalf("enroll start: status %d", code)
	}
	if code := desktop.do("POST", "/v1/runner/poll", nil, nil); code != 401 {
		t.Fatalf("runner polled before approval: status %d, want 401", code)
	}
	var pending remote.EnrollRequest
	if code := phone.do("GET", "/v1/enroll/"+ch.Code, nil, &pending); code != 200 || pending.Name != "Test Desktop" {
		t.Fatalf("enroll review: status %d, %+v", code, pending)
	}
	if code := phone.do("POST", "/v1/enroll/"+ch.Code+"/approve", nil, nil); code != 201 {
		t.Fatalf("enroll approve: status %d", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	submit := func(transcript, reqID string) (remote.Job, int) {
		var j remote.Job
		code := phone.do("POST", "/v1/jobs", remote.JobRequest{Transcript: transcript, ClientRequestID: reqID}, &j)
		return j, code
	}
	terminal := func(j remote.Job) bool {
		return j.State != remote.StateCancelRequest && len(j.Events) > 0 && strings.Contains("succeeded failed cancelled expired needs_reconciliation", j.State)
	}

	t.Run("transcribe via Deepgram", func(t *testing.T) {
		var out struct{ Transcript string }
		if code := phone.do("POST", "/v1/transcribe", []byte("fake-audio"), &out); code != 200 || out.Transcript != "run the tests" {
			t.Fatalf("status %d, transcript %q", code, out.Transcript)
		}
	})

	var firstJob remote.Job
	t.Run("routine tool runs under the standing grant", func(t *testing.T) {
		j, code := submit("run the tests", "req-1")
		if code != 201 {
			t.Fatalf("submit: status %d", code)
		}
		if again, code := submit("run the tests", "req-1"); code != 200 || again.ID != j.ID {
			t.Fatalf("replayed submission made a new job: status %d", code)
		}
		if _, code := submit("something else", "req-1"); code != 409 {
			t.Fatalf("reused request ID with a new payload: status %d, want 409", code)
		}
		j = phone.waitJob(j.ID, "completion", terminal)
		if j.State != remote.StateSucceeded || len(j.Actions) != 1 {
			t.Fatalf("job %s (%s), actions %d", j.State, j.Detail, len(j.Actions))
		}
		a := j.Actions[0]
		if a.Status != remote.ActionDone || a.ExitCode == nil || *a.ExitCode != 3 || !strings.Contains(a.Output, "tests ran") {
			t.Fatalf("action %+v", a)
		}
		if strings.Contains(a.Output, "leaked") {
			t.Fatal("the test command saw the runner's environment")
		}
		if !strings.Contains(j.Result, "exit code 3") || j.RunnerName != "Test Desktop" {
			t.Fatalf("result %q on %q", j.Result, j.RunnerName)
		}
		firstJob = j
	})

	t.Run("out-of-scope arguments never reach the runner", func(t *testing.T) {
		j, _ := submit("read my secrets", "req-2")
		j = phone.waitJob(j.ID, "completion", terminal)
		if len(j.Actions) != 0 || !strings.Contains(j.Result, "not enrolled") {
			t.Fatalf("actions %d, result %q", len(j.Actions), j.Result)
		}
	})

	t.Run("sensitive tool pauses for a single-use approval", func(t *testing.T) {
		j, _ := submit("open the app", "req-3")
		j = phone.waitJob(j.ID, "approval request", func(j remote.Job) bool { return j.State == remote.StateApproval })
		a := j.Actions[0]
		if a.Status != remote.ActionAwaiting || a.Challenge == "" {
			t.Fatalf("action %+v", a)
		}
		approve := func(challenge string) int {
			return phone.do("POST", "/v1/jobs/"+j.ID+"/approval", map[string]any{"challenge": challenge, "approve": true}, nil)
		}
		if code := approve("wrong"); code != 409 {
			t.Fatalf("wrong challenge: status %d, want 409", code)
		}
		if code := approve(a.Challenge); code != 200 {
			t.Fatalf("approve: status %d", code)
		}
		if code := approve(a.Challenge); code != 409 {
			t.Fatalf("challenge reuse: status %d, want 409", code)
		}
		j = phone.waitJob(j.ID, "completion", terminal)
		if j.State != remote.StateSucceeded || j.Actions[0].Status != remote.ActionDone {
			t.Fatalf("job %s, action %s", j.State, j.Actions[0].Status)
		}
	})

	t.Run("cancel stops a running action", func(t *testing.T) {
		j, _ := submit("run the slow tests", "req-4")
		phone.waitJob(j.ID, "dispatch", func(j remote.Job) bool {
			return len(j.Actions) == 1 && j.Actions[0].Status == remote.ActionDispatched
		})
		var after remote.Job
		if code := phone.do("POST", "/v1/jobs/"+j.ID+"/cancel", nil, &after); code != 200 || after.State != remote.StateCancelRequest {
			t.Fatalf("cancel: status %d, state %s", code, after.State)
		}
		j = phone.waitJob(j.ID, "stop", terminal)
		if j.State != remote.StateCancelled || j.Actions[0].Status != remote.ActionCancelled {
			t.Fatalf("job %s, action %s", j.State, j.Actions[0].Status)
		}
	})

	t.Run("a runner without the lease cannot commit results", func(t *testing.T) {
		res := remote.RunnerResult{ActionID: firstJob.Actions[0].ID, JobID: firstJob.ID, Lease: "stale", Status: remote.ActionDone}
		if code := desktop.do("POST", "/v1/runner/result", res, nil); code != 409 {
			t.Fatalf("status %d, want 409", code)
		}
	})

	t.Run("a revoked phone is rejected", func(t *testing.T) {
		if code := phone.do("DELETE", "/v1/devices/"+remote.DeviceID(phone.priv.Public().(ed25519.PublicKey)), nil, nil); code != 200 {
			t.Fatalf("revoke: status %d", code)
		}
		if _, code := submit("run the tests", "req-5"); code != 401 {
			t.Fatalf("submit after revocation: status %d, want 401", code)
		}
	})
}

// A runner that takes an action and then vanishes leaves an uncertain effect.
// The job must not be retried blindly, the late result must be refused, and
// the state must survive a coordinator restart.
func TestLostRunnerNeedsReconciliation(t *testing.T) {
	cfg := remote.Config{
		DBPath: filepath.Join(t.TempDir(), "remote.db"), Engine: scriptedPlanner{}, Model: "test",
		PollWait: 100 * time.Millisecond, LeaseTTL: 300 * time.Millisecond,
	}
	coord, err := remote.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(coord.Routes())
	defer srv.Close()

	phone, desktop := client{t, srv.URL, newKey(t)}, client{t, srv.URL, newKey(t)}
	phone.do("POST", "/v1/pair", remote.PairRequest{Code: coord.NewPairCode(), Name: "Phone", PublicKey: phone.priv.Public().(ed25519.PublicKey)}, nil)
	var ch remote.EnrollChallenge
	desktop.do("POST", "/v1/enroll", remote.EnrollRequest{
		Name: "Flaky", Kind: remote.KindDesktop, PublicKey: desktop.priv.Public().(ed25519.PublicKey),
		Tools: []string{"repo_run_tests"}, Workspaces: []string{"demo"},
	}, &ch)
	if code := phone.do("POST", "/v1/enroll/"+ch.Code+"/approve", nil, nil); code != 201 {
		t.Fatalf("enroll approve: status %d", code)
	}

	var j remote.Job
	phone.do("POST", "/v1/jobs", remote.JobRequest{Transcript: "run the tests", ClientRequestID: "r1"}, &j)
	// The runner has never connected, so the job waits and says why.
	j = phone.waitJob(j.ID, "waiting_for_target", func(j remote.Job) bool { return j.State == remote.StateWaitingTarget })
	if !strings.Contains(j.Detail, "Flaky") {
		t.Fatalf("wait reason %q does not name the device", j.Detail)
	}

	// The runner claims the job, takes the action, and goes silent.
	var work remote.PollResponse
	for len(work.Actions) == 0 {
		if code := desktop.do("POST", "/v1/runner/poll", nil, &work); code != 200 {
			t.Fatalf("poll: status %d", code)
		}
	}
	j = phone.waitJob(j.ID, "needs_reconciliation", func(j remote.Job) bool { return j.State == remote.StateReconciliation })
	if j.Actions[0].Status != remote.ActionUnknown || len(j.Actions) != 1 {
		t.Fatalf("actions %+v", j.Actions)
	}

	// The stale runner comes back with its result: fenced.
	a := work.Actions[0]
	late := remote.RunnerResult{ActionID: a.ID, JobID: a.JobID, Lease: a.Lease, Status: remote.ActionDone}
	if code := desktop.do("POST", "/v1/runner/result", late, nil); code != 409 {
		t.Fatalf("late result: status %d, want 409", code)
	}

	// Durable across a restart.
	srv.Close()
	coord.Close()
	coord, err = remote.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer coord.Close()
	srv2 := httptest.NewServer(coord.Routes())
	defer srv2.Close()
	phone.base = srv2.URL
	var again remote.Job
	if code := phone.do("GET", "/v1/jobs/"+j.ID, nil, &again); code != 200 || again.State != remote.StateReconciliation || len(again.Events) != len(j.Events) {
		t.Fatalf("after restart: status %d, state %s", code, again.State)
	}
}
