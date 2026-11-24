//go:build !windows

package core

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// sharedSocketMode is the API socket's mode when agents of run_as_user
// projects call it; each request is then checked by its caller's uid.
const sharedSocketMode fs.FileMode = 0o666

// checkSocketFolder checks that no user but root and cc-connect's own can
// change dir, a folder of the API socket, or any folder above it: each must be
// owned by root or by uid self and have no group or other write bit. read
// reads each folder.
func checkSocketFolder(dir string, self uint32, read func(string) (fs.FileInfo, error)) error {
	for d := dir; ; d = filepath.Dir(d) {
		info, err := read(d)
		if err != nil {
			return fmt.Errorf("check API socket folder %s: %w", d, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("check API socket folder %s: no owner information", d)
		}
		if st.Uid != 0 && st.Uid != self {
			return fmt.Errorf("API socket folder %s is owned by uid %d; every folder above the socket must be owned by root or by cc-connect's user (uid %d). Set data_dir to a folder that meets this", d, st.Uid, self)
		}
		if mode := info.Mode().Perm(); mode&0o022 != 0 {
			return fmt.Errorf("API socket folder %s has mode %o, so other users can write it. Run `chmod go-w %s`, or set data_dir to a folder whose parents only root can write", d, mode, d)
		}
		if d == filepath.Dir(d) {
			return nil
		}
	}
}

// maxSocketLinks caps the links checkSocketLinks follows, so a link loop
// ends.
const maxSocketLinks = 40

// checkSocketLinks follows the links on path one at a time, from / down, and
// checks with checkSocketFolder the folder that holds each, so no link on the
// way to the socket, including one met inside another link's target, sits in
// a folder another user can change. lstat reads each path.
func checkSocketLinks(path string, self uint32, lstat func(string) (fs.FileInfo, error)) error {
	for links := 0; ; links++ {
		if links > maxSocketLinks {
			return fmt.Errorf("check API socket folder %s: too many links", path)
		}
		parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
		held, next := "/", ""
		for i, part := range parts {
			p := filepath.Join(held, part)
			info, err := lstat(p)
			if err != nil {
				return fmt.Errorf("check API socket folder %s: %w", p, err)
			}
			if info.Mode()&fs.ModeSymlink == 0 {
				held = p
				continue
			}
			if err := checkSocketFolder(held, self, lstat); err != nil {
				return err
			}
			target, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("check API socket folder %s: %w", p, err)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(held, target)
			}
			next = filepath.Join(append([]string{target}, parts[i+1:]...)...)
			break
		}
		if next == "" {
			return nil
		}
		path = next
	}
}

// listenSharedSocket opens the API socket in runDir for the agents' users.
// Clients reach the socket through runDir as configured, so it first checks
// the folders on that path with stat, which follows links, and the folder
// holding each link on the way with checkSocketLinks. It then resolves runDir
// through symlinks, checks the resolved folders, listens there, makes the
// socket mode 0666 and checks the socket file again, reading each with lstat.
// It returns the listener and the socket's resolved path.
func listenSharedSocket(runDir string, lstat, stat func(string) (fs.FileInfo, error)) (net.Listener, string, error) {
	self := uint32(os.Geteuid())
	configured, err := filepath.Abs(runDir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve API socket folder %s: %w", runDir, err)
	}
	if err := checkSocketFolder(configured, self, stat); err != nil {
		return nil, "", err
	}
	if err := checkSocketLinks(configured, self, lstat); err != nil {
		return nil, "", err
	}
	dir, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve API socket folder %s: %w", runDir, err)
	}
	if err := checkSocketFolder(dir, self, lstat); err != nil {
		return nil, "", err
	}
	sockPath := filepath.Join(dir, "api.sock")
	if err := os.Remove(sockPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("remove stale API socket %s: %w", sockPath, err)
	}
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, "", fmt.Errorf("listen on API socket %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, sharedSocketMode); err != nil {
		_ = listener.Close()
		return nil, "", fmt.Errorf("chmod API socket %s: %w", sockPath, err)
	}
	if err := checkSocketFile(sockPath, self, lstat); err != nil {
		_ = listener.Close()
		return nil, "", err
	}
	return listener, sockPath, nil
}

// checkSocketFile checks that path is a socket owned by uid self with mode
// 0666.
func checkSocketFile(path string, self uint32, lstat func(string) (fs.FileInfo, error)) error {
	info, err := lstat(path)
	if err != nil {
		return fmt.Errorf("check API socket %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Type() != fs.ModeSocket || !ok || st.Uid != self || info.Mode().Perm() != sharedSocketMode {
		return fmt.Errorf("API socket %s changed after it was opened (mode %s); another process may have replaced it", path, info.Mode())
	}
	return nil
}
