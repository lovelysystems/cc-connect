package core

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerCredSocketPeerPidIsTheConnectingProcess(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server := <-accepted
	defer func() { _ = server.Close() }()

	pid, err := socketPeerPid(server)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Fatalf("peer pid = %d, want %d", pid, os.Getpid())
	}
	parent, err := processParentPid(os.Getpid())
	if err != nil || parent != os.Getppid() {
		t.Fatalf("parent pid = %d, %v; want %d", parent, err, os.Getppid())
	}
}

func TestPeerCredSignoffCallerCheckReadsTheRequestsConnection(t *testing.T) {
	server, _ := unixSocketPair(t)
	r := httptest.NewRequest(http.MethodPost, "/signoff/send", nil)
	r = r.WithContext(context.WithValue(r.Context(), apiConnKey{}, server))
	if !callerUnderAgent(r, os.Getpid()) {
		t.Fatal("a caller that is the agent process failed the caller check")
	}
}
