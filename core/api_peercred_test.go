package core

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// unixSocketPair returns both ends of a fresh Unix socket connection.
func unixSocketPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	dir, err := os.MkdirTemp("", "peercred")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
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
	client, err = net.Dial("unix", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server = <-accepted
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return server, client
}

func TestPeerCredSocketPeerUIDIsTheConnectingUser(t *testing.T) {
	if !SocketPeerCredentialsSupported() {
		t.Skip("no socket peer credentials on this platform")
	}
	server, _ := unixSocketPair(t)
	uid, err := socketPeerUID(server)
	if err != nil {
		t.Fatal(err)
	}
	if uid != os.Geteuid() {
		t.Fatalf("peer uid = %d, want %d", uid, os.Geteuid())
	}
}
