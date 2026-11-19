//go:build !windows

package core

import (
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
