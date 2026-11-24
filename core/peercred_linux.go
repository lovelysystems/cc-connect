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

// SocketPeerCredentialsSupported reports whether the API socket can tell
// which user is calling.
func SocketPeerCredentialsSupported() bool { return true }

// socketPeerUID returns the uid of the process on the other end of a Unix
// socket connection.
func socketPeerUID(c net.Conn) (int, error) {
	cred, err := socketPeerUcred(c)
	if err != nil {
		return 0, err
	}
	return int(cred.Uid), nil
}

// socketPeerPid returns the pid of the process on the other end of a Unix
// socket connection.
func socketPeerPid(c net.Conn) (int, error) {
	cred, err := socketPeerUcred(c)
	if err != nil {
		return 0, err
	}
	return int(cred.Pid), nil
}

func socketPeerUcred(c net.Conn) (*syscall.Ucred, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil, errors.New("not a unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	if credErr != nil {
		return nil, credErr
	}
	return cred, nil
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
