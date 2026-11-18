package runner

import (
	"os/exec"
	"strconv"
)

// ownProcessGroup makes cancellation kill the command and everything it spawned.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
