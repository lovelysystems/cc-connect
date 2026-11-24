package core

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// SocketPeerCredentialsSupported reports whether the API socket can tell
// which user is calling.
func SocketPeerCredentialsSupported() bool { return true }

// socketPeerUID returns the uid of the process on the other end of a Unix
// socket connection.
func socketPeerUID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Uid), nil
}

// socketPeerPid fails on macOS, so the signoff caller check fails closed.
func socketPeerPid(net.Conn) (int, error) {
	return 0, errors.New("socket peer pid is only available on linux")
}

func processParentPid(int) (int, error) {
	return 0, errors.New("process parent pid is only available on linux")
}
