package core

import (
	"errors"
	"net"
	"net/http"
	"os"
)

// apiConnKey keys the API socket connection of a request in its context.
type apiConnKey struct{}

// requestPeerUID returns the uid of the process on the other end of r's API
// socket connection. Without peer credentials every caller counts as
// cc-connect's own user.
func requestPeerUID(r *http.Request, peerCredentials bool) (int, error) {
	if !peerCredentials {
		return os.Geteuid(), nil
	}
	conn, _ := r.Context().Value(apiConnKey{}).(net.Conn)
	if conn == nil {
		return 0, errors.New("request has no socket connection")
	}
	return socketPeerUID(conn)
}
