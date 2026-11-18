package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/chengyaolee/blooper/internal/llm"
	"github.com/chengyaolee/blooper/internal/remote"
)

func computerHelper() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "blooper-computer")
}

// RequestComputerPermissions is only called by the authenticated desktop UI,
// never by a phone or planner. macOS owns the actual permission dialogs.
func RequestComputerPermissions(ctx context.Context) error {
	_, err := callComputer(ctx, []byte(`{"action":"permissions"}`))
	return err
}

func callComputer(ctx context.Context, args []byte) (*remote.RunnerResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, computerHelper())
	cmd.Stdin, cmd.Env = bytes.NewReader(args), minimalEnv(nil)
	stdout, stderr := &tail{max: 800 << 10}, &tail{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("computer control: %w: %s", err, stderr.String())
	}
	var result remote.RunnerResult
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		return nil, fmt.Errorf("invalid computer helper response: %w", err)
	}
	if result.Image != nil {
		if err := remote.ValidateScreen(result.Image); err != nil {
			return nil, err
		}
	}
	return &result, nil
}

func (r *Runner) useComputer(ctx context.Context, jobID string, args json.RawMessage) (string, string, *llm.Image) {
	r.computerMu.Lock()
	defer r.computerMu.Unlock()
	if ctx.Err() != nil {
		return remote.ActionCancelled, "cancelled before computer action", nil
	}
	a, err := remote.ParseComputerAction(args)
	if err != nil {
		return remote.ActionFailed, err.Error(), nil
	}
	if a.Action != "screenshot" && r.observedJob != jobID {
		return remote.ActionFailed, "take a screenshot in this job before sending computer input", nil
	}
	nativeArgs, _ := json.Marshal(struct {
		remote.ComputerAction
		Width  int `json:"screen_width"`
		Height int `json:"screen_height"`
	}{a, r.screenWidth, r.screenHeight})
	result, err := callComputer(ctx, nativeArgs)
	if ctx.Err() != nil {
		r.observedJob = ""
		return remote.ActionCancelled, "cancelled; already delivered input cannot be undone", nil
	}
	if err != nil {
		r.observedJob = ""
		return remote.ActionFailed, err.Error() + "; do not repeat input: take a fresh screenshot before continuing", nil
	}
	if result.Image != nil {
		r.observedJob = jobID
		r.screenWidth, r.screenHeight = result.Image.Width, result.Image.Height
	} else {
		r.observedJob = ""
	}
	if a.Action == "screenshot" && result.Image == nil {
		return remote.ActionFailed, "computer helper returned no screenshot", nil
	}
	return remote.ActionDone, result.Output, result.Image
}
