//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// socketTempDir returns a temp folder short enough for a socket path.
func socketTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ccsock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func runAsConfig(dataDir string, users ...string) *config.Config {
	cfg := &config.Config{DataDir: dataDir}
	for i, u := range users {
		cfg.Projects = append(cfg.Projects, config.ProjectConfig{Name: string(rune('a' + i)), RunAsUser: u})
	}
	return cfg
}

func noProbe(t *testing.T) socketProbe {
	return func(context.Context, string, string) (int, error) {
		t.Fatal("probe called without run_as_user")
		return 0, nil
	}
}

func TestOpenAPISocket_WithoutRunAsIsOwnSocket(t *testing.T) {
	srv, err := openAPISocket(context.Background(), runAsConfig(socketTempDir(t), "", ""), true, noProbe(t))
	if err != nil || srv == nil {
		t.Fatalf("srv, err = %v, %v", srv, err)
	}
	defer srv.Stop()
	info, err := os.Stat(srv.SocketPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v; want 0600", info, err)
	}
}

func TestOpenAPISocket_WithoutRunAsFailureOnlyWarns(t *testing.T) {
	notADir := filepath.Join(socketTempDir(t), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := openAPISocket(context.Background(), runAsConfig(notADir, ""), true, noProbe(t))
	if err != nil || srv != nil {
		t.Fatalf("srv, err = %v, %v; want nil, nil", srv, err)
	}
}

func TestOpenAPISocket_RunAsWithoutPeerCredentialsStops(t *testing.T) {
	_, err := openAPISocket(context.Background(), runAsConfig(socketTempDir(t), "agent_a"), false, noProbe(t))
	if err == nil || !strings.Contains(err.Error(), runtime.GOOS) {
		t.Fatalf("err = %v, want it to name %s", err, runtime.GOOS)
	}
}

func TestOpenAPISocket_RunAsSocketFailureStops(t *testing.T) {
	notADir := filepath.Join(socketTempDir(t), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openAPISocket(context.Background(), runAsConfig(notADir, "agent_a"), true, noProbe(t)); err == nil {
		t.Fatal("want error when the socket cannot be opened")
	}
}

// sharedSocketDataDir returns a data_dir in which a shared socket opens, or
// skips the test when the temp folder's parents are writable by others.
func sharedSocketDataDir(t *testing.T) string {
	t.Helper()
	dir := socketTempDir(t)
	srv, err := core.NewAPIServer(dir, true)
	if err != nil {
		t.Skipf("temp folder cannot hold a shared socket: %v", err)
	}
	srv.Stop()
	return dir
}

func TestOpenAPISocket_RunAsProbesEachUserOnceAndSetsUIDs(t *testing.T) {
	dataDir := sharedSocketDataDir(t)
	calls := map[string]int{}
	probe := func(_ context.Context, user, sockPath string) (int, error) {
		calls[user]++
		if filepath.Base(sockPath) != "api.sock" {
			t.Errorf("probe got socket path %s", sockPath)
		}
		return map[string]int{"agent_a": 7101, "agent_b": 7102}[user], nil
	}
	cfg := runAsConfig(dataDir, "agent_a", "agent_b", "agent_b", "")
	srv, err := openAPISocket(context.Background(), cfg, true, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if calls["agent_a"] != 1 || calls["agent_b"] != 1 || len(calls) != 2 {
		t.Fatalf("probe calls = %v, want one per user", calls)
	}
	info, err := os.Stat(srv.SocketPath())
	if err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode = %v, %v; want 0666", info, err)
	}
	for i, p := range cfg.Projects {
		e := core.NewEngine(p.Name, &stubMainAgent{}, nil, "", core.LangEnglish)
		t.Cleanup(func() { _ = e.Stop() })
		e.SetRunAs(cfg.Projects[i].RunAsUser, nil)
		srv.RegisterEngine(p.Name, e)
	}
	if _, projects := srv.CallerProjects(7102); strings.Join(projects, ",") != "b,c" {
		t.Fatalf("agent_b acts for %v, want [b c]", projects)
	}
}

func TestOpenAPISocket_RunAsProbeFailureStops(t *testing.T) {
	dataDir := sharedSocketDataDir(t)
	probe := func(_ context.Context, user, _ string) (int, error) {
		if user == "agent_b" {
			return 0, errors.New("user \"agent_b\" cannot enter /srv; set data_dir")
		}
		return 7101, nil
	}
	_, err := openAPISocket(context.Background(), runAsConfig(dataDir, "agent_a", "agent_b"), true, probe)
	if err == nil || !strings.Contains(err.Error(), "cannot enter /srv") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(dataDir, "run", "api.sock")); statErr == nil {
		t.Fatal("socket left open after a failed probe")
	}
}

func writeConfigFile(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("data_dir = \"x\"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckConfigFile_ReadableInsideDataDirStops(t *testing.T) {
	dataDir := t.TempDir()
	configPath := writeConfigFile(t, dataDir, 0o644)
	err := checkConfigFile(configPath, dataDir)
	if err == nil {
		t.Fatal("want an error for a readable config inside data_dir")
	}
	for _, want := range []string{configPath, "outside data_dir", "chmod 600"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestCheckConfigFile_PrivateOrOutsideDataDirPasses(t *testing.T) {
	dataDir := t.TempDir()
	if err := checkConfigFile(writeConfigFile(t, dataDir, 0o600), dataDir); err != nil {
		t.Errorf("0600 config inside data_dir: %v", err)
	}
	if err := checkConfigFile(writeConfigFile(t, t.TempDir(), 0o644), dataDir); err != nil {
		t.Errorf("0644 config outside data_dir: %v", err)
	}
}

func TestRunRunAsUserStartupChecks_WithoutRunAsIgnoresConfigMode(t *testing.T) {
	dataDir := t.TempDir()
	configPath := writeConfigFile(t, dataDir, 0o644)
	if err := runRunAsUserStartupChecks(context.Background(), runAsConfig(dataDir, ""), configPath); err != nil {
		t.Fatalf("without run_as_user: %v", err)
	}
}
