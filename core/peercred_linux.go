package core

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// socketPeerPid returns the pid of the process on the other end of a Unix
// socket connection.
func socketPeerPid(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Pid), nil
}

// processParentPid reads the parent pid of pid from /proc.
func processParentPid(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// The command name in parentheses may itself hold spaces and ')'.
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	return strconv.Atoi(string(fields[1]))
}
