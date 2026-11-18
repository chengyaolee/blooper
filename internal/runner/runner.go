// Package runner is the process that executes jobs on an enrolled computer
// (or in a cloud workspace). It only makes outbound requests to the
// coordinator, and it only runs what its own config file allows: the planner
// picks a workspace or app by name, never a command.
package runner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chengyaolee/blooper/internal/remote"
	"github.com/chengyaolee/blooper/internal/tools"
	"gopkg.in/yaml.v3"
)

type Workspace struct {
	Path    string            `yaml:"path"`
	TestCmd []string          `yaml:"test_cmd"` // argv, run without a shell
	Timeout string            `yaml:"timeout"`  // Go duration; default 30m
	Env     map[string]string `yaml:"env"`      // added to the minimal environment
}

type Config struct {
	Coordinator        string               `yaml:"coordinator"`          // base URL, e.g. https://blooper.example.ts.net
	Name               string               `yaml:"name"`                 // shown on the phone; default: hostname
	Kind               string               `yaml:"kind"`                 // desktop (default) or cloud
	KeyFile            string               `yaml:"key_file"`             // default: runner.key beside the config
	Workspaces         map[string]Workspace `yaml:"workspaces"`           // enables repo_run_tests
	Apps               map[string][]string  `yaml:"apps"`                 // name -> argv; enables app_open
	WebSearch          bool                 `yaml:"web_search"`           // enables web_search
	Computer           bool                 `yaml:"computer"`             // opt-in main-display control (macOS)
	AutoApproveRoutine bool                 `yaml:"auto_approve_routine"` // opt-in routine computer steps and configured app launches
	// AllowInsecure permits an http:// coordinator URL that is not loopback.
	// Without TLS, anyone on the network path can forge actions to this runner.
	AllowInsecure bool `yaml:"allow_insecure"`
}

// checkCoordinatorURL refuses plain HTTP to anything but loopback unless the
// owner has explicitly opted in.
func checkCoordinatorURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("coordinator %q is not a URL", raw)
	}
	if u.Scheme == "https" || allowInsecure {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return fmt.Errorf("coordinator %q uses plain HTTP; use https, or set allow_insecure: true to accept the risk", raw)
}

type Runner struct {
	cfg                       Config
	priv                      ed25519.PrivateKey
	client                    *http.Client
	mu                        sync.Mutex
	workers                   sync.WaitGroup
	running                   map[string]context.CancelFunc // in-flight actions
	computerMu                sync.Mutex
	observedJob               string
	screenWidth, screenHeight int
}

// Load reads the config and loads or creates the runner's key.
func Load(path string) (*Runner, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Coordinator = strings.TrimRight(cfg.Coordinator, "/")
	if cfg.Coordinator == "" {
		return nil, fmt.Errorf("%s: coordinator URL is required", path)
	}
	if err := checkCoordinatorURL(cfg.Coordinator, cfg.AllowInsecure); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
	}
	if cfg.Kind == "" {
		cfg.Kind = remote.KindDesktop
	}
	if cfg.AutoApproveRoutine && !cfg.Computer {
		return nil, fmt.Errorf("auto_approve_routine requires computer: true")
	}
	if cfg.Computer && (runtime.GOOS != "darwin" || cfg.Kind != remote.KindDesktop) {
		return nil, fmt.Errorf("computer control requires a macOS desktop runner")
	}
	if cfg.Computer {
		if _, err := os.Stat(computerHelper()); err != nil {
			return nil, fmt.Errorf("computer helper is missing; build with npm run build:runtime in desktop")
		}
	}
	for name, ws := range cfg.Workspaces {
		if ws.Path == "" || len(ws.TestCmd) == 0 {
			return nil, fmt.Errorf("%s: workspace %q needs path and test_cmd", path, name)
		}
		if _, err := time.ParseDuration(ws.Timeout); ws.Timeout != "" && err != nil {
			return nil, fmt.Errorf("%s: workspace %q: %w", path, name, err)
		}
	}
	for name, argv := range cfg.Apps {
		if len(argv) == 0 {
			return nil, fmt.Errorf("%s: app %q needs a command", path, name)
		}
	}
	if cfg.KeyFile == "" {
		cfg.KeyFile = filepath.Join(filepath.Dir(path), "runner.key")
	}
	priv, err := loadKey(cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	// The poll is held for up to 25s by the coordinator.
	return &Runner{cfg: cfg, priv: priv, client: &http.Client{Timeout: 60 * time.Second}, running: map[string]context.CancelFunc{}}, nil
}

// ponytail: the key is a 0600 file. Move it to the OS keystore (Keychain,
// DPAPI, Secret Service) when a second user shares the machine.
func loadKey(path string) (ed25519.PrivateKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is not a runner key", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	seed := make([]byte, ed25519.SeedSize)
	rand.Read(seed)
	if err := os.WriteFile(path, []byte(hex.EncodeToString(seed)), 0600); err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func (r *Runner) capabilities() remote.EnrollRequest {
	req := remote.EnrollRequest{
		AutoApproveRoutine: r.cfg.AutoApproveRoutine,
		Name:               r.cfg.Name, Kind: r.cfg.Kind, OS: runtime.GOOS,
		PublicKey: r.priv.Public().(ed25519.PublicKey),
		Tools:     []string{}, Workspaces: []string{}, Apps: []string{},
	}
	for name := range r.cfg.Workspaces {
		req.Workspaces = append(req.Workspaces, name)
	}
	for name := range r.cfg.Apps {
		req.Apps = append(req.Apps, name)
	}
	sort.Strings(req.Workspaces)
	sort.Strings(req.Apps)
	if len(req.Workspaces) > 0 {
		req.Tools = append(req.Tools, "repo_run_tests")
	}
	if len(req.Apps) > 0 {
		req.Tools = append(req.Tools, "app_open")
	}
	if r.cfg.WebSearch {
		req.Tools = append(req.Tools, "web_search")
	}
	if r.cfg.Computer {
		req.Tools = append(req.Tools, "computer")
	}
	return req
}

// call sends a signed request. out is filled on 2xx.
func (r *Runner) call(ctx context.Context, method, uri string, in, out any) (int, error) {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	req, err := remote.SignedRequest(method, r.cfg.Coordinator, uri, body, r.priv)
	if err != nil {
		return 0, err
	}
	resp, err := r.client.Do(req.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s", bytes.TrimSpace(data))
	}
	if out != nil {
		return resp.StatusCode, json.Unmarshal(data, out)
	}
	return resp.StatusCode, nil
}

// Run polls the coordinator until ctx ends. An unenrolled (or revoked) runner
// stops its work and shows an enrollment QR for the phone to scan.
func (r *Runner) Run(ctx context.Context) error {
	defer func() { r.stopAll(); r.workers.Wait() }()
	log.Printf("runner %q (%s) -> %s", r.cfg.Name, remote.DeviceID(r.priv.Public().(ed25519.PublicKey)), r.cfg.Coordinator)
	var codeExpires time.Time
	enrolled := false
	for ctx.Err() == nil {
		var resp remote.PollResponse
		status, err := r.call(ctx, http.MethodPost, "/v1/runner/poll", nil, &resp)
		switch {
		case status == http.StatusUnauthorized:
			r.stopAll() // revoked: stop executing at once
			enrolled = false
			if time.Now().After(codeExpires) {
				codeExpires = r.enroll(ctx)
			}
			sleep(ctx, 2*time.Second)
		case err != nil:
			log.Printf("poll: %v", err)
			sleep(ctx, 3*time.Second)
		default:
			if !enrolled {
				enrolled = true
				log.Printf("enrolled and connected; waiting for jobs")
			}
			for _, id := range resp.Cancel {
				r.stop(id)
			}
			for _, a := range resp.Actions {
				r.workers.Add(1)
				go func(a remote.RunnerAction) { defer r.workers.Done(); r.execute(ctx, a) }(a)
			}
		}
	}
	return ctx.Err()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (r *Runner) enroll(ctx context.Context) time.Time {
	var ch remote.EnrollChallenge
	if _, err := r.call(ctx, http.MethodPost, "/v1/enroll", r.capabilities(), &ch); err != nil {
		log.Printf("enrollment failed: %v (check the coordinator URL and this machine's clock)", err)
		return time.Now().Add(10 * time.Second)
	}
	fmt.Printf("\nThis computer is not enrolled. In the blooper Remote app, open Devices > Add computer and scan:\n\n")
	remote.PrintQR(os.Stdout, "blooper://enroll?code="+ch.Code)
	fmt.Printf("\nOr type the code: %s (valid until %s)\n\n", ch.Code, time.Unix(ch.ExpiresAt, 0).Format("15:04"))
	return time.Unix(ch.ExpiresAt, 0)
}

func (r *Runner) stop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel := r.running[id]; cancel != nil {
		cancel()
	}
}

func (r *Runner) stopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.running {
		cancel()
	}
}

func (r *Runner) execute(parent context.Context, a remote.RunnerAction) {
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	r.running[a.ID] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, a.ID)
		r.mu.Unlock()
		cancel()
	}()

	res := remote.RunnerResult{ActionID: a.ID, JobID: a.JobID, Lease: a.Lease, Status: remote.ActionFailed}
	arg, err := remote.ParseArg(a.Tool, a.Args)
	// Computer arguments may include private text; keep them out of diagnostics.
	log.Printf("action %s: %s", a.ID, a.Tool)
	// Local policy is enforced here, whatever the coordinator asked for.
	switch {
	case err != nil:
		res.Output = err.Error()
	case a.Tool == "computer" && r.cfg.Computer:
		res.Status, res.Output, res.Image = r.useComputer(ctx, a.JobID, a.Args)
	case a.Tool == "repo_run_tests":
		res.Status, res.ExitCode, res.Output = r.runTests(ctx, arg)
	case a.Tool == "app_open":
		res.Status, res.ExitCode, res.Output = r.openApp(arg)
	case a.Tool == "web_search" && r.cfg.WebSearch:
		res.Status, res.Output = webSearch(ctx, a.Args)
	default:
		res.Output = "tool " + a.Tool + " is not enabled on this runner"
	}

	// Report until the coordinator has it. A 4xx means the lease is gone and
	// this runner may no longer commit the result.
	for parent.Err() == nil {
		status, err := r.call(parent, http.MethodPost, "/v1/runner/result", res, nil)
		if err == nil || status/100 == 4 {
			if err != nil {
				log.Printf("action %s: result refused: %v", a.ID, err)
			}
			return
		}
		sleep(parent, 2*time.Second)
	}
}

// minimalEnv is the least-privilege environment for test commands: enough to
// find tools, none of the runner's secrets.
func minimalEnv(extra map[string]string) []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "USER", "LANG", "TMPDIR", "TEMP", "TMP", "SystemRoot", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ComSpec", "PATHEXT"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (r *Runner) runTests(ctx context.Context, name string) (status string, exit *int, output string) {
	ws, ok := r.cfg.Workspaces[name]
	if !ok {
		return remote.ActionFailed, nil, "workspace " + name + " is not configured on this runner"
	}
	timeout := 30 * time.Minute
	if ws.Timeout != "" {
		timeout, _ = time.ParseDuration(ws.Timeout)
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &tail{max: 64 << 10}
	cmd := exec.CommandContext(tctx, ws.TestCmd[0], ws.TestCmd[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = ws.Path, minimalEnv(ws.Env), out, out
	cmd.WaitDelay = 5 * time.Second
	ownProcessGroup(cmd)
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return remote.ActionCancelled, nil, out.String()
	case tctx.Err() != nil:
		return remote.ActionFailed, nil, fmt.Sprintf("timed out after %s\n%s", timeout, out)
	case cmd.ProcessState == nil:
		return remote.ActionFailed, nil, "could not start: " + err.Error()
	}
	code := cmd.ProcessState.ExitCode()
	return remote.ActionDone, &code, out.String()
}

// openApp launches an allow-listed app in this user's desktop session. The
// runner must itself be running in that session.
// ponytail: no lock-screen or GUI-readiness check; the launch just fails or
// waits behind the lock screen. Add per-OS session checks when it matters.
func (r *Runner) openApp(name string) (status string, exit *int, output string) {
	argv, ok := r.cfg.Apps[name]
	if !ok {
		return remote.ActionFailed, nil, "app " + name + " is not allow-listed on this runner"
	}
	out := &tail{max: 4 << 10}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return remote.ActionFailed, nil, "could not start: " + err.Error()
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done: // a launcher such as `open -a` exits straight away
		code := cmd.ProcessState.ExitCode()
		return remote.ActionDone, &code, strings.TrimSpace("launcher exited\n" + out.String())
	case <-time.After(3 * time.Second):
		return remote.ActionDone, nil, "launched; still running after 3s"
	}
}

func webSearch(ctx context.Context, args json.RawMessage) (status, output string) {
	res, err := tools.NewWebSearchTool().Execute(ctx, args)
	switch {
	case err != nil:
		return remote.ActionFailed, err.Error()
	case res.Error != "":
		return remote.ActionFailed, res.Error
	}
	return remote.ActionDone, res.Output
}

// tail is a writer that keeps only the last max bytes.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// Capabilities describes the local runner without exposing filesystem paths.
func (r *Runner) Capabilities() remote.EnrollRequest { return r.capabilities() }

// SetHTTPClient installs the desktop's pinned TLS transport before Run starts.
func (r *Runner) SetHTTPClient(client *http.Client) { r.client = client }
