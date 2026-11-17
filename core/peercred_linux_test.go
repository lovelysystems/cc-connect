package core

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestProcessParentPidReadsTheParent(t *testing.T) {
	got, err := processParentPid(os.Getpid())
	if err != nil {
		t.Fatalf("processParentPid: %v", err)
	}
	if want := os.Getppid(); got != want {
		t.Fatalf("processParentPid = %d, want %d", got, want)
	}
}

func TestSocketPeerPidReadsTheCaller(t *testing.T) {
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	server, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer server.Close()

	got, err := socketPeerPid(server)
	if err != nil {
		t.Fatalf("socketPeerPid: %v", err)
	}
	if got != os.Getpid() {
		t.Fatalf("socketPeerPid = %d, want %d", got, os.Getpid())
	}
}
