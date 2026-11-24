package core

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
)

// socketPeerPid returns the pid of the process on the other end of a Unix
// socket connection.
func socketPeerPid(c net.Conn) (int, error) {
	cred, err := socketPeerUcred(c)
	if err != nil {
		return 0, err
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
