//go:build windows

package core

import (
	"errors"
	"io/fs"
	"net"
)

// listenSharedSocket fails on Windows, where run_as_user is not supported.
func listenSharedSocket(string, func(string) (fs.FileInfo, error), func(string) (fs.FileInfo, error)) (net.Listener, string, error) {
	return nil, "", errors.New("a shared API socket is not supported on Windows")
}
