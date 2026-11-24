//go:build !windows

package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	err := e.runShellWithProgress(p, "ctx", `echo started; echo "$$" > `+shellQuote(pidFile)+`; sleep 30`, "", "", time.Second, 4000)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout took %s", d)
	}
	waitGone(t, pidFile)
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

type replyCtxPlatform struct {
	stubPlatformEngine
}

func (p *replyCtxPlatform) ReconstructReplyCtx(string) (any, error) { return "rctx", nil }

func TestWebhookExecuteShell_RunAsRunsAsProjectUser(t *testing.T) {
	p := &replyCtxPlatform{stubPlatformEngine{n: "test"}}
	e, _ := newRunAsEngine(t, "noisy", p, &stubAgent{})

	NewWebhookServer(0, "", "").executeShell(e, WebhookRequest{SessionKey: "test:c:u", Exec: envProbe, Silent: true}, "push")
	got := waitSent(t, &p.stubPlatformEngine, "✅")
	if !strings.Contains(got, "target|proj|test:c:u|/var/lib/cc-test") || strings.Contains(got, "noise") || strings.Contains(got, "@@") {
		t.Fatalf("webhook reply = %q", got)
	}
}

func TestWebhookExecuteShell_WithoutRunAsRunsAsOwnUser(t *testing.T) {
	p := &replyCtxPlatform{stubPlatformEngine{n: "test"}}
	e, sudo := newRunAsEngine(t, "exec", p, &stubAgent{})
	e.SetRunAs("", nil)
	t.Setenv("CC_PROJECT", "from-own-env")

	NewWebhookServer(0, "", "").executeShell(e, WebhookRequest{SessionKey: "test:c:u", Exec: envProbe + "; echo err >&2", WorkDir: t.TempDir(), Silent: true}, "push")
	got := waitSent(t, &p.stubPlatformEngine, "✅")
	if !strings.Contains(got, "|from-own-env||\nerr") {
		t.Fatalf("webhook reply = %q", got)
	}
	if calls := sudo.calls(t); calls != "" {
		t.Fatalf("sudo called without run_as_user: %s", calls)
	}
}

func TestHookCommand_RunsAsProjectUser(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hook.out")
	probe := `printf '%s|%s|%s|%s|%s\n' "$CC_FAKE_SUDO_USER" "$CC_HOOK_EVENT" "$CC_HOOK_PROJECT" "$CC_PROJECT" "$CC_OWN_ONLY" > ` + shellQuote(out)
	t.Setenv("CC_OWN_ONLY", "own")
	for _, tc := range []struct {
		name  string
		runAs string
		want  string
	}{
		{"run as", "target", "target|session.started|proj|proj|\n"},
		{"own user", "", "|session.started|proj||own\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newRunAsEngine(t, "noisy", &stubPlatformEngine{n: "test"}, &stubAgent{})
			e.SetRunAs(tc.runAs, nil)
			async := false
			e.SetHooks(NewHookManager("proj", []HookConfig{{Event: "session.started", Type: "command", Command: probe, Async: &async}}, "sh", "-c", ""))
			e.hooks.Emit(HookEvent{Event: HookEventSessionStarted, SessionKey: "test:c:u"})
			got, err := os.ReadFile(out)
			if err != nil || string(got) != tc.want {
				t.Fatalf("hook wrote %q (%v), want %q", got, err, tc.want)
			}
		})
	}
}

// newDiffRepo makes a git repo with one committed file and an uncommitted
// change to it.
func newDiffRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("zero\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "f.txt"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type fileSenderPlatform struct {
	stubPlatformEngine
	files []FileAttachment
}

func (p *fileSenderPlatform) SendFile(_ context.Context, _ any, f FileAttachment) error {
	p.files = append(p.files, f)
	return nil
}

func TestCmdDiff_RunAsRunsGitAsProjectUser(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newDiffRepo(t)
	p := &fileSenderPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e, sudo := newRunAsEngine(t, "noisy", p, &stubWorkDirAgent{workDir: repo})
	t.Setenv("GIT_DIR", "/nonexistent")

	e.cmdDiff(p, &Message{SessionKey: "test:c:u", ReplyCtx: "ctx"}, "/diff")
	got := waitSent(t, &p.stubPlatformEngine, "```diff")
	if !strings.Contains(got, "+one") || strings.Contains(got, "noise") || strings.Contains(got, "@@cc-connect") {
		t.Fatalf("diff reply = %q", got)
	}
	if hint := e.i18n.T(MsgDiffNoDiff2HTML); !slices.Contains(p.getSent(), hint) {
		t.Fatalf("missing diff2html hint %q; sent %q", hint, p.getSent())
	}
	if n := strings.Count(sudo.calls(t), "-- exec /bin/sh -c"); n != 4 {
		t.Fatalf("want three git commands and diff2html through sudo, log:\n%s", sudo.calls(t))
	}
}

func TestCmdDiff_RunAsBranchNameHasNoNoise(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := newDiffRepo(t)
	p := &stubPlatformEngine{n: "test"}
	e, _ := newRunAsEngine(t, "noisy", p, &stubWorkDirAgent{workDir: repo})
	out, err := e.projectOutput(context.Background(), repo, "", nil, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || string(out) != "main\n" {
		t.Fatalf("branch = %q, %v", out, err)
	}
}

// installFakeGit puts a git on the target's PATH that records its arguments
// and creates the clone's folder, or with hang set never returns.
func installFakeGit(t *testing.T, hang bool) (binDir, log string) {
	t.Helper()
	binDir = t.TempDir()
	log = filepath.Join(binDir, "git.log")
	body := `printf '%s\n' "$*" > ` + shellQuote(log) + `; mkdir -p "$3"`
	if hang {
		body = `echo "$$" > ` + shellQuote(filepath.Join(binDir, "pid")) + `; sleep 60`
	}
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return binDir, log
}

func TestWorkspaceInit_RunAsClonesAsProjectUser(t *testing.T) {
	gitDir, gitLog := installFakeGit(t, false)
	sudo := installFakeSudoWithPath(t, "noisy", gitDir+":/usr/bin:/bin")
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("proj", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetRunAs("target", nil)
	baseDir := t.TempDir()
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))

	msg := &Message{SessionKey: "test:ch1:u1", Content: "/workspace init https://example.com/org/repo.git", ReplyCtx: "ctx", UserID: "u1"}
	e.handleCommand(p, msg, msg.Content)

	dest := filepath.Join(baseDir, "repo")
	if got, _ := os.ReadFile(gitLog); string(got) != "clone https://example.com/org/repo.git "+dest+"\n" {
		t.Fatalf("git args = %q", got)
	}
	if !strings.Contains(sudo.calls(t), "-iu target") {
		t.Fatalf("clone did not go through sudo: %s", sudo.calls(t))
	}
	if b := e.workspaceBindings.Lookup("project:proj", workspaceChannelKey("test", "ch1")); b == nil || b.Workspace != normalizeWorkspacePath(dest) {
		t.Fatalf("binding = %+v", b)
	}

	// The folder now exists: a second init binds it and does not clone.
	_ = os.Remove(gitLog)
	e.handleCommand(p, msg, msg.Content)
	if _, err := os.Stat(gitLog); err == nil {
		t.Fatal("existing folder was cloned again")
	}
	if !strings.Contains(p.getSent()[len(p.getSent())-1], dest) {
		t.Fatalf("last reply = %q", p.getSent()[len(p.getSent())-1])
	}
}

func TestWorkspaceInitFlow_RunAsSkipGitCreatesFolderAsProjectUser(t *testing.T) {
	sudo := installFakeSudo(t, "exec")
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("proj", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetRunAs("target", nil)
	e.SetSkipGit(true)
	baseDir := t.TempDir()
	e.SetMultiWorkspace(baseDir, filepath.Join(t.TempDir(), "bindings.json"))
	dest := filepath.Join(baseDir, "new ws")
	msg := &Message{SessionKey: "test:ch1:u1", Content: "yes", ReplyCtx: "ctx"}
	e.initFlows[effectiveWorkspaceChannelKey(msg)] = &workspaceInitFlow{state: "awaiting_confirm", cloneTo: dest, channelName: "ch"}

	if !e.handleWorkspaceInitFlow(p, msg, "ch") {
		t.Fatal("flow not consumed")
	}
	if info, err := os.Stat(dest); err != nil || !info.IsDir() {
		t.Fatalf("folder not created: %v", err)
	}
	if !strings.Contains(sudo.calls(t), "-iu target") {
		t.Fatalf("mkdir did not go through sudo: %s", sudo.calls(t))
	}
}

func TestWorkspaceInit_RunAsHangingCloneStops(t *testing.T) {
	gitDir, _ := installFakeGit(t, true)
	installFakeSudoWithPath(t, "noisy", gitDir+":/usr/bin:/bin")
	old := workspaceCloneTimeout
	workspaceCloneTimeout = time.Second
	t.Cleanup(func() { workspaceCloneTimeout = old })
	e := NewEngine("proj", &stubAgent{}, []Platform{&stubPlatformEngine{n: "test"}}, "", LangEnglish)
	e.SetRunAs("target", nil)

	start := time.Now()
	err := e.createWorkspaceAsUser("https://example.com/r.git", filepath.Join(t.TempDir(), "r"), false, "")
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
	waitGone(t, filepath.Join(gitDir, "pid"))
}
