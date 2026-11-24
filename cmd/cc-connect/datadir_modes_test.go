package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTightenDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions not supported on Windows")
	}
	dataDir := t.TempDir()
	write := func(rel string, mode os.FileMode) string {
		path := filepath.Join(dataDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	session := write("sessions/demo_1234.json", 0o644)
	cron := write("crons/jobs.json", 0o644)
	relay := write("relay_bindings.json", 0o644)
	legacySession := write("demo.sessions.json", 0o644)
	socket := write("run/api.sock", 0o666)
	qqbotCache := write("run/qqbot_message_cache.json", 0o644)
	prompt := write("agent-prompts/cc-connect-system.md", 0o644)
	config := write("config.toml", 0o644)

	tightenDataDir(dataDir)

	for path, want := range map[string]os.FileMode{
		session:               0o600,
		filepath.Dir(session): 0o700,
		cron:                  0o600,
		filepath.Dir(cron):    0o700,
		relay:                 0o600,
		legacySession:         0o600,
		socket:                0o666,
		filepath.Dir(socket):  0o755,
		qqbotCache:            0o600,
		prompt:                0o644,
		filepath.Dir(prompt):  0o755,
		config:                0o644,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %#o, want %#o", path, got, want)
		}
	}
}
