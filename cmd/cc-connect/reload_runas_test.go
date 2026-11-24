package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestReloadConfigKeepsTheSocketScopeOfRunAsUser(t *testing.T) {
	const uidA, uidB = 7101, 7102
	dir, err := os.MkdirTemp("", "ccreload")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	engine := core.NewEngine("a", &stubMainAgent{}, nil, "", core.LangEnglish)
	t.Cleanup(func() { _ = engine.Stop() })
	engine.SetRunAs("agent_a", nil)

	api, err := core.NewAPIServer(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	api.Start()
	t.Cleanup(api.Stop)
	api.RegisterEngine("a", engine)
	api.SetRunAsUserIDs(map[string]int{"agent_a": uidA, "agent_b": uidB})

	configPath := filepath.Join(dir, "config.toml")
	changed := `
[[projects]]
name = "a"
run_as_user = "agent_b"

[projects.agent]
type = "claudecode"

[[projects.platforms]]
type = "telegram"
`
	if err := os.WriteFile(configPath, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reloadConfig(configPath, "a", engine); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}

	if _, projects := api.CallerProjects(uidA); !slices.Equal(projects, []string{"a"}) {
		t.Fatalf("after reload agent_a acts for %v, want [a]", projects)
	}
	if _, projects := api.CallerProjects(uidB); len(projects) != 0 {
		t.Fatalf("after reload agent_b acts for %v, want none", projects)
	}
}
