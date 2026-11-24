package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestPeerCredSignoffCallerCheckFailsClosedOnMacOS(t *testing.T) {
	server, _ := unixSocketPair(t)
	if pid, err := socketPeerPid(server); err == nil {
		t.Fatalf("socket peer pid = %d on macOS, want an error", pid)
	}
	r := httptest.NewRequest(http.MethodPost, "/signoff/send", nil)
	r = r.WithContext(context.WithValue(r.Context(), apiConnKey{}, server))
	if callerUnderAgent(r, os.Getpid()) {
		t.Fatal("the signoff caller check passed on macOS")
	}
}
