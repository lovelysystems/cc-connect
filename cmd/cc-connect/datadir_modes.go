package main

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// tightenDataDir makes the stores cc-connect keeps in dataDir readable by
// cc-connect's own user only: their folders become 0700 and their files 0600.
// The run/ folder with the API socket and the agent-prompts/ folder keep their
// modes, since run_as_user agents must reach the socket and read the prompt
// files. Missing paths are skipped, and a path that cannot be changed is logged.
func tightenDataDir(dataDir string) {
	for _, name := range []string{"sessions", "projects", "crons", "timers", "teams", "weixin"} {
		_ = filepath.WalkDir(filepath.Join(dataDir, name), func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				if !errors.Is(err, fs.ErrNotExist) {
					slog.Warn("data_dir: cannot read store folder", "path", path, "error", err)
				}
			case d.IsDir():
				chmodStore(path, 0o700)
			case d.Type().IsRegular():
				chmodStore(path, 0o600)
			}
			return nil
		})
	}
	files, _ := filepath.Glob(filepath.Join(dataDir, "*.json"))
	files = append(files, filepath.Join(dataDir, "run", "qqbot_message_cache.json"))
	for _, path := range files {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			chmodStore(path, 0o600)
		}
	}
}

func chmodStore(path string, mode os.FileMode) {
	if err := os.Chmod(path, mode); err != nil {
		slog.Warn("data_dir: cannot tighten store mode", "path", path, "mode", mode, "error", err)
	}
}
