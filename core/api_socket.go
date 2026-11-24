//go:build !windows

package core

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// sharedSocketMode is the API socket's mode when agents of run_as_user
// projects call it; each request is then checked by its caller's uid.
const sharedSocketMode fs.FileMode = 0o666

// checkSocketFolder checks that no user but root and cc-connect's own can
// change dir, the resolved folder of the API socket, or any folder above it:
// each must be owned by root or by uid self and have no group or other write
// bit. lstat reads each folder.
func checkSocketFolder(dir string, self uint32, lstat func(string) (fs.FileInfo, error)) error {
	for d := dir; ; d = filepath.Dir(d) {
		info, err := lstat(d)
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

// listenSharedSocket opens the API socket in runDir for the agents' users:
// it resolves runDir through symlinks, checks the resolved folders, listens
// there, makes the socket mode 0666 and checks the socket file again, reading
// each with lstat. It returns the listener and the socket's resolved path.
func listenSharedSocket(runDir string, lstat func(string) (fs.FileInfo, error)) (net.Listener, string, error) {
	dir, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return nil, "", fmt.Errorf("resolve API socket folder %s: %w", runDir, err)
	}
	self := uint32(os.Geteuid())
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
