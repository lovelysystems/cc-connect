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
