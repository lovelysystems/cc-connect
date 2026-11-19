//go:build windows

package core

import "os/exec"

// killProcessGroupOnCancel keeps exec's default on Windows: the process
// itself is killed when cmd's context is done.
func killProcessGroupOnCancel(*exec.Cmd) {}
