//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup makes cancellation kill the command and everything it spawned.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
