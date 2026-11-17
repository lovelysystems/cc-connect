//go:build !linux

package core

import (
	"errors"
	"net"
)

func socketPeerPid(net.Conn) (int, error) {
	return 0, errors.New("socket peer pid is only available on linux")
}

func processParentPid(int) (int, error) {
	return 0, errors.New("process parent pid is only available on linux")
}
