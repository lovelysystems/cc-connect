//go:build !windows

package core

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newSignoffExecEngine(t *testing.T, command string) *Engine {
	t.Helper()
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetShell("sh", "-c", "")
	e.SetSignoffExec(command)
	return e
}

var signoffExecTestInput = signoffExecInput{
	RequestID: "r1",
	Nonce:     "n1",
	ClickedBy: SignoffClicker{ID: "aad-1", Name: "Jane Editor"},
}

func TestSignoffExecPassesTheClickAndReadsTheOutcome(t *testing.T) {
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin.json")
	e := newSignoffExecEngine(t, `cat > `+stdin+`; echo '{"outcome":"published","message":"5 changes live"}'`)

	outcome, message := e.runSignoffExec("", signoffExecTestInput)

	if outcome != SignoffPublished || message != "5 changes live" {
		t.Fatalf("outcome, message = %q, %q; want published, 5 changes live", outcome, message)
	}
	got, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"request_id":"r1","nonce":"n1","clicked_by":{"id":"aad-1","name":"Jane Editor"}}`
	if string(got) != want {
		t.Fatalf("stdin = %s\nwant    %s", got, want)
	}
}

func TestSignoffExecRefusedWithoutMessage(t *testing.T) {
	e := newSignoffExecEngine(t, `echo '{"outcome":"refused"}'`)
	if outcome, message := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffRefused || message != "" {
		t.Fatalf("outcome, message = %q, %q; want refused, empty", outcome, message)
	}
}

func TestSignoffExecRunsFromRoot(t *testing.T) {
	e := newSignoffExecEngine(t, `[ "$(pwd)" = / ] && echo '{"outcome":"published"}'`)
	if outcome, _ := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffPublished {
		t.Fatalf("outcome = %q, want published from /", outcome)
	}
}

func TestSignoffExecFailures(t *testing.T) {
	cases := map[string]string{
		"exits non-zero":       `echo '{"outcome":"published"}'; exit 1`,
		"prints plain text":    `echo ok`,
		"prints two objects":   `echo '{"outcome":"published"}{"outcome":"refused"}'`,
		"prints text after":    `echo '{"outcome":"published"} ok'`,
		"unknown outcome":      `echo '{"outcome":"done"}'`,
		"unknown field":        `echo '{"outcome":"published","extra":1}'`,
		"message line break":   `printf '%s\n' '{"outcome":"published","message":"a\nb"}'`,
		"message carriage ret": `printf '%s\n' '{"outcome":"published","message":"a\rb"}'`,
		"message line sep":     `printf '%s\n' '{"outcome":"published","message":"a\u2028b"}'`,
		"message para sep":     `printf '%s\n' '{"outcome":"published","message":"a\u2029b"}'`,
		"prints nothing":       `true`,
	}
	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			e := newSignoffExecEngine(t, command)
			if outcome, message := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffFailed || message != "" {
				t.Fatalf("outcome, message = %q, %q; want failed, empty", outcome, message)
			}
		})
	}
}

func TestSignoffExecUnsetFails(t *testing.T) {
	e := newSignoffExecEngine(t, "")
	if outcome, _ := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffFailed {
		t.Fatalf("outcome = %q, want failed", outcome)
	}
}

func TestSignoffExecLimitKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	late := filepath.Join(dir, "late")
	e := newSignoffExecEngine(t, `(sleep 1; touch `+late+`) & sleep 5; echo '{"outcome":"published"}'`)
	e.signoff.execLimit = 200 * time.Millisecond

	start := time.Now()
	outcome, _ := e.runSignoffExec("", signoffExecTestInput)
	if outcome != SignoffFailed {
		t.Fatalf("outcome = %q, want failed", outcome)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("runner returned after %v, want soon after the limit", elapsed)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(late); err == nil {
		t.Fatal("a child of the command ran on after the limit")
	}
}

func TestSignoffExecFailedExitKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	late := filepath.Join(dir, "late")
	e := newSignoffExecEngine(t, `(sleep 1; touch `+late+`) >/dev/null 2>&1 & exit 1`)

	if outcome, _ := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffFailed {
		t.Fatalf("outcome = %q, want failed", outcome)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(late); err == nil {
		t.Fatal("a child of the command ran on after the command failed")
	}
}

func TestSignoffExecOutcomeArrivesWhileADescendantHoldsStderr(t *testing.T) {
	e := newSignoffExecEngine(t, `sleep 10 >/dev/null & echo '{"outcome":"published","message":"live"}'`)

	done := make(chan [2]string, 1)
	go func() {
		outcome, message := e.runSignoffExec("", signoffExecTestInput)
		done <- [2]string{outcome, message}
	}()
	select {
	case got := <-done:
		if got[0] != SignoffPublished || got[1] != "live" {
			t.Fatalf("outcome, message = %q, %q; want published, live", got[0], got[1])
		}
	case <-time.After(6 * time.Second):
		t.Fatal("runner waited for a descendant holding stderr")
	}
}

func TestSignoffExecOutcomeCountsWhenTheShellExitedZeroAndADescendantOutlivesTheLimit(t *testing.T) {
	e := newSignoffExecEngine(t, `sleep 5 & echo '{"outcome":"published","message":"live"}'`)
	e.signoff.execLimit = 300 * time.Millisecond

	if outcome, message := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffPublished || message != "live" {
		t.Fatalf("outcome, message = %q, %q; want published, live", outcome, message)
	}
}

func TestSignoffExecSkipsTheShellProfile(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "profile-ran")
	e := newSignoffExecEngine(t, `echo '{"outcome":"published"}'`)
	e.SetShell("sh", "-c", "touch "+marker)

	if outcome, _ := e.runSignoffExec("", signoffExecTestInput); outcome != SignoffPublished {
		t.Fatalf("outcome = %q, want published", outcome)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("shell_profile ran before signoff_exec")
	}
}
