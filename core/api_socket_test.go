//go:build !windows

package core

import (
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeFileInfo struct {
	name string
	mode fs.FileMode
	uid  uint32
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

// fakeFolders answers lstat for the folders in tree and for every other
// path as a root-owned 0755 folder.
func fakeFolders(tree map[string]fakeFileInfo) func(string) (fs.FileInfo, error) {
	return func(p string) (fs.FileInfo, error) {
		if info, ok := tree[p]; ok {
			return info, nil
		}
		return fakeFileInfo{name: p, mode: fs.ModeDir | 0o755}, nil
	}
}

func TestCheckSocketFolder(t *testing.T) {
	const self = 1000
	for _, tc := range []struct {
		name    string
		tree    map[string]fakeFileInfo
		wantErr string
	}{
		{"root and self owned 0755 chain", map[string]fakeFileInfo{"/srv/cc/run": {mode: fs.ModeDir | 0o700, uid: self}}, ""},
		{"group-writable folder two levels up", map[string]fakeFileInfo{"/srv": {mode: fs.ModeDir | 0o775}}, "/srv has mode 775"},
		{"world-writable sticky folder", map[string]fakeFileInfo{"/srv": {mode: fs.ModeDir | fs.ModeSticky | 0o777}}, "/srv has mode 777"},
		{"folder owned by another uid", map[string]fakeFileInfo{"/srv/cc": {mode: fs.ModeDir | 0o755, uid: 1234}}, "/srv/cc is owned by uid 1234"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSocketFolder("/srv/cc/run", self, fakeFolders(tc.tree))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// folderLstat answers lstat for the folders above the socket as root-owned
// 0755 folders, records each path, and reads the socket itself from disk.
func folderLstat(seen *[]string) func(string) (fs.FileInfo, error) {
	return func(p string) (fs.FileInfo, error) {
		*seen = append(*seen, p)
		if filepath.Base(p) == "api.sock" {
			return os.Lstat(p)
		}
		return fakeFileInfo{name: p, mode: fs.ModeDir | 0o755}, nil
	}
}

// shortTempDir returns a temp folder short enough for a socket path on
// macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListenSharedSocket_ResolvesLinkAndOpens0666(t *testing.T) {
	base := shortTempDir(t)
	real := filepath.Join(base, "real-run")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "run")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	var seen []string
	l, sockPath, err := listenSharedSocket(link, folderLstat(&seen))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	resolved, _ := filepath.EvalSymlinks(real)
	if sockPath != filepath.Join(resolved, "api.sock") {
		t.Fatalf("socket path = %s, want it in %s", sockPath, resolved)
	}
	for _, p := range seen {
		if strings.HasPrefix(p, link) {
			t.Fatalf("checked %s through the link", p)
		}
	}
	info, err := os.Lstat(sockPath)
	if err != nil || info.Mode().Perm() != 0o666 || info.Mode().Type() != fs.ModeSocket {
		t.Fatalf("socket = %v, %v; want a 0666 socket", info, err)
	}
}

func TestListenSharedSocket_FailsWhenSocketFileIsReplaced(t *testing.T) {
	runDir := shortTempDir(t)
	lstat := func(p string) (fs.FileInfo, error) {
		if filepath.Base(p) == "api.sock" {
			return fakeFileInfo{name: p, mode: 0o666, uid: uint32(os.Geteuid())}, nil
		}
		return fakeFileInfo{name: p, mode: fs.ModeDir | 0o755}, nil
	}
	l, _, err := listenSharedSocket(runDir, lstat)
	if err == nil {
		_ = l.Close()
		t.Fatal("want error for a socket replaced by a regular file")
	}
	if !strings.Contains(err.Error(), "changed after it was opened") {
		t.Fatalf("err = %v", err)
	}
}

func TestListenSharedSocket_UnsafeFolderOpensNothing(t *testing.T) {
	runDir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(runDir)
	parent := filepath.Dir(resolved)
	_, _, err := listenSharedSocket(runDir, fakeFolders(map[string]fakeFileInfo{parent: {mode: fs.ModeDir | 0o777}}))
	if err == nil || !strings.Contains(err.Error(), parent) {
		t.Fatalf("err = %v, want it to name %s", err, parent)
	}
	if _, err := os.Lstat(filepath.Join(resolved, "api.sock")); err == nil {
		t.Fatal("socket created in an unsafe folder")
	}
}

// probeSocket returns a socket path for ProbeSocketAccess and the name of
// the user running the test, which the fake sudo runs as.
func probeSocket(t *testing.T) (sock, self string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	sock = filepath.Join(t.TempDir(), "run", "api.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	return sock, u.Username
}

func TestProbeSocketAccess(t *testing.T) {
	installFakeSudo(t, "noisy")
	sock, self := probeSocket(t)

	uid, err := ProbeSocketAccess(t.Context(), self, sock)
	if err != nil || uid != os.Getuid() {
		t.Fatalf("uid, err = %d, %v; want %d, nil", uid, err, os.Getuid())
	}

	if os.Geteuid() == 0 {
		t.Skip("root enters any folder")
	}
	locked := filepath.Dir(sock)
	if err := os.Chmod(locked, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	_, err = ProbeSocketAccess(t.Context(), self, sock)
	if err == nil || !strings.Contains(err.Error(), "cannot enter "+locked) || !strings.Contains(err.Error(), "data_dir") {
		t.Fatalf("err = %v, want it to name %s and the fix", err, locked)
	}
}

func TestProbeSocketAccess_UIDIgnoresTheUsersProfile(t *testing.T) {
	shadow := t.TempDir()
	if err := os.WriteFile(filepath.Join(shadow, "id"), []byte("#!/bin/sh\necho 1050\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	installFakeSudoWithPath(t, "noisy", shadow+":/usr/bin:/bin")
	sock, self := probeSocket(t)

	uid, err := ProbeSocketAccess(t.Context(), self, sock)
	if err != nil || uid != os.Getuid() {
		t.Fatalf("uid, err = %d, %v; want %d, nil", uid, err, os.Getuid())
	}
}
