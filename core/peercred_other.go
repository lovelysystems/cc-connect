//go:build !linux && !darwin

package core

import (
	"errors"
	"net"
)

// SocketPeerCredentialsSupported reports whether the API socket can tell
// which user is calling.
func SocketPeerCredentialsSupported() bool { return false }

func socketPeerUID(net.Conn) (int, error) {
	return 0, errors.New("socket peer credentials are not available on this platform")
}

// socketPeerPid fails outside Linux, so the signoff caller check fails closed.
func socketPeerPid(net.Conn) (int, error) {
	return 0, errors.New("socket peer pid is only available on linux")
}

func processParentPid(int) (int, error) {
	return 0, errors.New("process parent pid is only available on linux")
}
