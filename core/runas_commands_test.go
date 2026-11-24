//go:build !windows

package core

import (
	"strings"
	"testing"
	"time"
)

// newRunAsEngine returns an engine whose project runs as "target" through the
// fake sudo in the given mode, with sh as its shell and dataDir set.
func newRunAsEngine(t *testing.T, mode string, p Platform, agent Agent) (*Engine, *fakeSudo) {
	t.Helper()
	sudo := installFakeSudo(t, mode)
	e := NewEngine("proj", agent, []Platform{p}, "", LangEnglish)
	e.SetShell("sh", "-c", "")
	e.SetDataDir("/var/lib/cc-test")
	e.SetRunAs("target", nil)
	return e, sudo
}

func waitSent(t *testing.T, p *stubPlatformEngine, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		sent := p.getSent()
		for _, s := range sent {
			if strings.Contains(s, want) {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no message with %q; sent %q", want, sent)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const envProbe = `printf '%s|%s|%s|%s\n' "$CC_FAKE_SUDO_USER" "$CC_PROJECT" "$CC_SESSION_KEY" "$CC_DATA_DIR"`

func TestExecuteCronShell_RunsAsProjectUser(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e, _ := newRunAsEngine(t, "noisy", p, &stubAgent{})

	if err := e.executeCronShell(p, "ctx", &CronJob{Exec: envProbe, SessionKey: "test:c:u"}); err != nil {
		t.Fatal(err)
	}
	got := waitSent(t, p, "✅")
	if !strings.Contains(got, "target|proj|test:c:u|/var/lib/cc-test") {
		t.Fatalf("cron output = %q", got)
	}
	if strings.Contains(got, "noise") {
		t.Fatalf("login noise in cron output: %q", got)
	}
}

func TestExecuteCronShell_WithoutRunAsKeepsOwnUserAndEnv(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e, sudo := newRunAsEngine(t, "exec", p, &stubAgent{})
	e.SetRunAs("", nil)
	t.Setenv("CC_PROJECT", "from-own-env")

	if err := e.executeCronShell(p, "ctx", &CronJob{Exec: envProbe, SessionKey: "test:c:u", WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	got := waitSent(t, p, "✅")
	if !strings.Contains(got, "|from-own-env||") {
		t.Fatalf("cron output = %q", got)
	}
	if calls := sudo.calls(t); calls != "" {
		t.Fatalf("sudo called without run_as_user: %s", calls)
	}
}

func TestExecuteTimerShell_RunAsUsesSh(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e, _ := newRunAsEngine(t, "exec", p, &stubAgent{})
	e.SetShell("bash", "-c", "")

	if err := e.executeTimerShell(p, "ctx", &TimerJob{Exec: `echo "shell=$0 user=$CC_FAKE_SUDO_USER"`}); err != nil {
		t.Fatal(err)
	}
	if got := waitSent(t, p, "✅"); !strings.Contains(got, "shell=sh user=target") {
		t.Fatalf("timer output = %q", got)
	}
}

func TestCmdShell_RunAsRepliesWithoutNoise(t *testing.T) {
	for _, raw := range []string{"/shell " + envProbe, "!" + envProbe} {
		t.Run(raw[:1], func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			e, _ := newRunAsEngine(t, "noisy", p, &stubWorkDirAgent{workDir: t.TempDir()})
			msg := &Message{SessionKey: "test:ch1:u1", Content: raw, ReplyCtx: "ctx"}
			if raw[0] == '!' {
				raw = "/shell " + raw[1:]
			}
			e.cmdShell(p, msg, raw)
			got := waitSent(t, p, "✅")
			if !strings.Contains(got, "target|proj|test:ch1:u1|/var/lib/cc-test") || strings.Contains(got, "noise") {
				t.Fatalf("reply = %q", got)
			}
		})
	}
}

func TestRunShellWithProgress_RunAsTimeoutStops(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e, _ := newRunAsEngine(t, "noisy", p, &stubAgent{})

	start := time.Now()
	err := e.runShellWithProgress(p, "ctx", "echo started; sleep 30", "", "", time.Second, 4000)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout took %s", d)
	}
	got := waitSent(t, p, "(timeout)")
	if !strings.Contains(got, "started") || strings.Contains(got, "noise") {
		t.Fatalf("timeout message = %q", got)
	}
}

func TestRunShellWithProgress_RunAsUpdatesPreview(t *testing.T) {
	p := &stubCompactProgressPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("proj", &stubAgent{}, []Platform{p}, "", LangEnglish)
	installFakeSudo(t, "noisy")
	e.SetShell("sh", "-c", "")
	e.SetRunAs("target", nil)

	if err := e.runShellWithProgress(p, "ctx", "echo one; sleep 3; echo two", "", "", 10*time.Second, 4000); err != nil {
		t.Fatal(err)
	}
	p.previewMu.Lock()
	defer p.previewMu.Unlock()
	if len(p.previewStarts) != 1 || !strings.Contains(p.previewStarts[0], "one") {
		t.Fatalf("preview starts = %q", p.previewStarts)
	}
	last := p.previewEdits[len(p.previewEdits)-1]
	if !strings.Contains(last, "one\ntwo") || strings.Contains(last, "noise") {
		t.Fatalf("final edit = %q", last)
	}
}
