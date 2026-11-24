//go:build !windows

package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newRunAsSignoffExecEngine(t *testing.T, mode, command string) (*Engine, *fakeSudo) {
	t.Helper()
	sudo := installFakeSudo(t, mode)
	e := newSignoffExecEngine(t, command)
	e.SetRunAs("target", nil)
	return e, sudo
}

func TestSignoffExecRunAsRunsAsProjectUserDespiteLoginNoise(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin.json")
	e, _ := newRunAsSignoffExecEngine(t, "noisy",
		`cat > `+stdin+`; echo err >&2; printf '{"outcome":"published","message":"%s %s"}\n' "$CC_FAKE_SUDO_USER" "$CC_SESSION_KEY"`)

	outcome, message := e.runSignoffExec("teams:c:u", signoffExecTestInput)

	if outcome != SignoffPublished || message != "target teams:c:u" {
		t.Fatalf("outcome, message = %q, %q; want published, %q", outcome, message, "target teams:c:u")
	}
	got, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"request_id":"r1","nonce":"n1","clicked_by":{"id":"aad-1","name":"Jane Editor"}}`
	if string(got) != want {
		t.Fatalf("stdin = %s, want %s", got, want)
	}
}

func TestSignoffExecRunAsPastLimitFailsAndStops(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	e, _ := newRunAsSignoffExecEngine(t, "noisy", `trap '' TERM; echo "$$" > `+pidFile+`; sleep 30; echo '{"outcome":"published"}'`)
	e.signoff.execLimit = time.Second

	start := time.Now()
	if outcome, _ := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffFailed {
		t.Fatalf("outcome = %q, want failed", outcome)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runner returned after %v, want soon after the limit", elapsed)
	}
	waitGone(t, pidFile)
}

func TestSignoffExecRunAsFailedSudoCheckRunsNothing(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	e, sudo := newRunAsSignoffExecEngine(t, "fail", `touch `+ran+`; echo '{"outcome":"published"}'`)

	if outcome, _ := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffFailed {
		t.Fatalf("outcome = %q, want failed", outcome)
	}
	if strings.Contains(sudo.calls(t), "/bin/sh") {
		t.Fatalf("signoff_exec ran after a failed check: %s", sudo.calls(t))
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("signoff_exec ran after a failed check")
	}
}
