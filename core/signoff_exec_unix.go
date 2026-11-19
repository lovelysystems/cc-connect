//go:build !windows

package core

import (
	"errors"
	"log/slog"
	"os/exec"
	"syscall"
)

// killProcessGroupOnCancel starts cmd in a process group of its own and kills
// the whole group when cmd's context is done, so no child outlives it.
func killProcessGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// killProcessGroup kills what is left of the process group cmd started.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Warn("signoff: kill signoff_exec process group failed", "pid", cmd.Process.Pid, "error", err)
	}
}
