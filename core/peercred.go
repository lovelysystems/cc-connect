package core

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
)

// maxAncestryDepth caps the walk up a caller's parents.
const maxAncestryDepth = 64

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

// callerUnderAgent reports whether the process on the other end of r's API
// socket connection is agentPid or runs under it. It keeps a card from landing
// in another session's thread by mistake; agents running as one user can pass
// it for one another.
func callerUnderAgent(r *http.Request, agentPid int) bool {
	conn, _ := r.Context().Value(apiConnKey{}).(net.Conn)
	if conn == nil {
		return false
	}
	pid, err := socketPeerPid(conn)
	if err != nil {
		slog.Warn("signoff: socket peer pid unavailable", "error", err)
		return false
	}
	return descendsFrom(pid, agentPid, processParentPid)
}

// descendsFrom reports whether ancestor is pid or one of its parents, looked
// up with parentPid. Any lookup error, or an ancestor of 0, gives false.
func descendsFrom(pid, ancestor int, parentPid func(int) (int, error)) bool {
	if ancestor <= 0 {
		return false
	}
	for depth := 0; pid > 0 && depth < maxAncestryDepth; depth++ {
		if pid == ancestor {
			return true
		}
		if pid == 1 {
			return false
		}
		next, err := parentPid(pid)
		if err != nil {
			return false
		}
		pid = next
	}
	return false
}
