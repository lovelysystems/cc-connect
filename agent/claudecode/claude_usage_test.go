package claudecode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestSanitizeClaudeUsageOutput_RendersCursorMoves(t *testing.T) {
	raw := "" +
		"\x1b[?2026h\rSettings:\x1b[1CStatus\x1b[1CConfig\x1b[1CUsage (tab to cycle)\r\n" +
		"\x1b]8;;https://code.claude.com/docs/en/security\aSecurity guide\x1b]8;;\a\r\n" +
		"Current session\r\n" +
		"\x1b[32m1% used\x1b[0m\r\n"

	terminal := newClaudeUsageTerminal()
	terminal.Write([]byte(raw))
	got := normalizeClaudeUsageText(terminal.String())
	if strings.Contains(got, "\x1b") {
		t.Fatalf("sanitizeClaudeUsageOutput still contains escape codes: %q", got)
	}
	if !strings.Contains(got, "Settings: Status Config Usage (tab to cycle)") {
		t.Fatalf("sanitized output missing spaced header: %q", got)
	}
	if !strings.Contains(got, "Security guide") {
		t.Fatalf("sanitized output missing link text: %q", got)
	}
}

func TestParseClaudeUsageReport_Success(t *testing.T) {
	loc := mustLoadLocation(t, "Asia/Seoul")
	now := time.Date(2026, time.December, 22, 12, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
1% used
Resets 3:59pm (Asia/Seoul)

Current week (all models)
2% used
Resets Dec 23, 4:59pm (Asia/Seoul)

Extra usage
Extra usage not enabled
`)

	report, err := parseClaudeUsageReport(text, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport returned error: %v", err)
	}
	if report.Provider != "claudecode" {
		t.Fatalf("Provider = %q, want claudecode", report.Provider)
	}
	if len(report.Buckets) != 1 {
		t.Fatalf("Buckets = %d, want 1", len(report.Buckets))
	}
	if len(report.Buckets[0].Windows) != 2 {
		t.Fatalf("Windows = %d, want 2", len(report.Buckets[0].Windows))
	}

	session := report.Buckets[0].Windows[0]
	if session.Name != "Current session" {
		t.Fatalf("session.Name = %q", session.Name)
	}
	if session.WindowSeconds != 18000 {
		t.Fatalf("session.WindowSeconds = %d, want 18000", session.WindowSeconds)
	}
	if session.UsedPercent != 1 {
		t.Fatalf("session.UsedPercent = %d, want 1", session.UsedPercent)
	}
	if session.ResetAfterSeconds != 14340 {
		t.Fatalf("session.ResetAfterSeconds = %d, want 14340", session.ResetAfterSeconds)
	}

	week := report.Buckets[0].Windows[1]
	if week.Name != "Current week" {
		t.Fatalf("week.Name = %q", week.Name)
	}
	if week.WindowSeconds != 604800 {
		t.Fatalf("week.WindowSeconds = %d, want 604800", week.WindowSeconds)
	}
	if week.UsedPercent != 2 {
		t.Fatalf("week.UsedPercent = %d, want 2", week.UsedPercent)
	}
	if week.ResetAfterSeconds != 104340 {
		t.Fatalf("week.ResetAfterSeconds = %d, want 104340", week.ResetAfterSeconds)
	}
}

func TestParseClaudeUsageReport_MissingOptionalFields(t *testing.T) {
	loc := mustLoadLocation(t, "Asia/Seoul")
	now := time.Date(2026, time.December, 22, 12, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
15% used
Resets 3:00pm (Asia/Seoul)

Current week
47% used
Resets Dec 23, 11:30am (Asia/Seoul)
`)

	report, err := parseClaudeUsageReport(text, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport returned error: %v", err)
	}
	if report.Email != "" {
		t.Fatalf("Email = %q, want empty", report.Email)
	}
	if report.AccountID != "" {
		t.Fatalf("AccountID = %q, want empty", report.AccountID)
	}
	if report.UserID != "" {
		t.Fatalf("UserID = %q, want empty", report.UserID)
	}
	if report.Plan != "" {
		t.Fatalf("Plan = %q, want empty", report.Plan)
	}
	if len(report.Buckets) != 1 || len(report.Buckets[0].Windows) != 2 {
		t.Fatalf("unexpected bucket layout: %+v", report.Buckets)
	}
}

func TestParseClaudeUsageReport_UpgradeRequired(t *testing.T) {
	text := "Unknown command: /usage"
	_, err := parseClaudeUsageReport(text, time.Now())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "upgrade") {
		t.Fatalf("err = %v, want upgrade guidance", err)
	}
}

func TestParseClaudeUsageReport_LoginRequired(t *testing.T) {
	text := "Please run `claude auth login` to continue."
	_, err := parseClaudeUsageReport(text, time.Now())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "login") {
		t.Fatalf("err = %v, want login guidance", err)
	}
}

func TestParseClaudeUsageReport_MissingWindowFields(t *testing.T) {
	loc := mustLoadLocation(t, "Asia/Seoul")
	now := time.Date(2026, time.December, 22, 12, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
Resets 3:00pm (Asia/Seoul)

Current week
47% used
Resets Dec 23, 11:30am (Asia/Seoul)
`)

	_, err := parseClaudeUsageReport(text, now)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "current session") {
		t.Fatalf("err = %v, want current session parse error", err)
	}
}

func TestParseClaudeUsageReport_UnknownResetTimeDoesNotFail(t *testing.T) {
	loc := mustLoadLocation(t, "America/Los_Angeles")
	now := time.Date(2026, time.March, 30, 11, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
14% used
Resets later today somehow

Current week
33% used
Resets Apr 2, 4pm (America/Los_Angeles)
`)

	report, err := parseClaudeUsageReport(text, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport returned error: %v", err)
	}
	if len(report.Buckets) != 1 || len(report.Buckets[0].Windows) != 2 {
		t.Fatalf("unexpected bucket layout: %+v", report.Buckets)
	}
	session := report.Buckets[0].Windows[0]
	if session.UsedPercent != 14 {
		t.Fatalf("session.UsedPercent = %d, want 14", session.UsedPercent)
	}
	if session.ResetAfterSeconds != 0 || session.ResetAtUnix != 0 {
		t.Fatalf("session reset should be unknown, got %+v", session)
	}
}

func TestParseClaudeUsageReport_MissingResetTimeDoesNotFail(t *testing.T) {
	loc := mustLoadLocation(t, "America/Los_Angeles")
	now := time.Date(2026, time.March, 30, 11, 0, 0, 0, loc)
	text := strings.TrimSpace(`
Current session
14% used

Current week
33% used
Resets Apr 2, 4pm (America/Los_Angeles)
`)

	report, err := parseClaudeUsageReport(text, now)
	if err != nil {
		t.Fatalf("parseClaudeUsageReport returned error: %v", err)
	}
	session := report.Buckets[0].Windows[0]
	if session.UsedPercent != 14 {
		t.Fatalf("session.UsedPercent = %d, want 14", session.UsedPercent)
	}
	if session.ResetAfterSeconds != 0 || session.ResetAtUnix != 0 {
		t.Fatalf("session reset should be absent, got %+v", session)
	}
}

func TestParseClaudeUsageResetTime_AllowsWholeHourWithTimezone(t *testing.T) {
	loc := mustLoadLocation(t, "America/Los_Angeles")
	now := time.Date(2026, time.March, 30, 11, 0, 0, 0, loc)

	resetAt, err := parseClaudeUsageResetTime("2pm (America/Los_Angeles)", now)
	if err != nil {
		t.Fatalf("parseClaudeUsageResetTime returned error: %v", err)
	}

	want := time.Date(2026, time.March, 30, 14, 0, 0, 0, loc)
	if !resetAt.Equal(want) {
		t.Fatalf("resetAt = %v, want %v", resetAt, want)
	}
}

func TestParseClaudeUsageResetTime_AllowsMonthDayWholeHour(t *testing.T) {
	loc := mustLoadLocation(t, "America/Los_Angeles")
	now := time.Date(2026, time.March, 30, 11, 0, 0, 0, loc)

	resetAt, err := parseClaudeUsageResetTime("Apr 2, 4pm (America/Los_Angeles)", now)
	if err != nil {
		t.Fatalf("parseClaudeUsageResetTime returned error: %v", err)
	}

	want := time.Date(2026, time.April, 2, 16, 0, 0, 0, loc)
	if !resetAt.Equal(want) {
		t.Fatalf("resetAt = %v, want %v", resetAt, want)
	}
}

func TestAgentGetUsageSmoke(t *testing.T) {
	if os.Getenv("CC_CONNECT_SMOKE_CLAUDE_USAGE") == "" {
		t.Skip("set CC_CONNECT_SMOKE_CLAUDE_USAGE=1 to run")
	}
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skipf("environment not suitable: %v", err)
	}

	a := &Agent{workDir: ".", mode: "plan"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	report, err := a.GetUsage(ctx)
	if err != nil {
		t.Fatalf("GetUsage returned error: %v", err)
	}
	if report == nil || len(report.Buckets) == 0 {
		t.Fatalf("unexpected empty report: %+v", report)
	}
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

// writeStubClaude puts a `claude` into a new dir that records its working
// dir and arguments, one per line in brackets, then sleeps without drawing
// anything. GetUsage returns once its context ends; a probe whose claude
// exits on its own keeps GetUsage waiting on the pty reader.
func writeStubClaude(t *testing.T) (dir, record string) {
	t.Helper()
	dir = t.TempDir()
	record = filepath.Join(t.TempDir(), "claude.record")
	script := "#!/bin/sh\n{ pwd -P; for a in \"$@\"; do printf '[%s]\\n' \"$a\"; done; } > " + testShellQuote(record) + "\nexec sleep 3\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, record
}

func TestGetUsage_RunAsUserRunsProbeAsTarget(t *testing.T) {
	stubDir, record := writeStubClaude(t)
	sudo := installFakeSudo(t, "exec", stubDir)
	if _, err := exec.LookPath("claude"); err == nil {
		t.Fatal("supervisor PATH unexpectedly has claude")
	}
	a := &Agent{spawnOpts: core.SpawnOptions{RunAsUser: "target"}}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	if _, err := a.GetUsage(ctx); err == nil {
		t.Fatal("GetUsage succeeded against a stub that renders nothing")
	}

	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("stub claude did not run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	wantDir, err := filepath.EvalSymlinks(sudo.home)
	if err != nil {
		t.Fatal(err)
	}
	wantDir = filepath.Join(wantDir, ".cc-connect", "usage-probe")
	if lines[0] != wantDir {
		t.Fatalf("probe ran in %q, want %q", lines[0], wantDir)
	}
	wantArgs := []string{"[--tools]", "[]", "[--permission-mode]", "[plan]", "[--no-chrome]"}
	if got := lines[1:]; strings.Join(got, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("probe args = %v, want %v", got, wantArgs)
	}
	if !strings.Contains(sudo.calls(t), "-n -iu target") {
		t.Fatalf("probe did not go through sudo; sudo log:\n%s", sudo.calls(t))
	}
}

func TestGetUsage_RunAsUserSudoFailureStartsNoClaude(t *testing.T) {
	stubDir, record := writeStubClaude(t)
	installFakeSudo(t, "fail", "")
	t.Setenv("PATH", os.Getenv("PATH")+":"+stubDir)
	a := &Agent{spawnOpts: core.SpawnOptions{RunAsUser: "target"}}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	if _, err := a.GetUsage(ctx); err == nil {
		t.Fatal("GetUsage succeeded although sudo failed")
	}
	if _, err := os.Stat(record); err == nil {
		t.Fatal("claude started without sudo")
	}
}

func TestGetUsage_WithoutRunAsUserSkipsSudo(t *testing.T) {
	stubDir, record := writeStubClaude(t)
	sudo := installFakeSudo(t, "record", "")
	t.Setenv("PATH", os.Getenv("PATH")+":"+stubDir)
	a := &Agent{}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	_, _ = a.GetUsage(ctx)

	if _, err := os.Stat(record); err != nil {
		t.Fatalf("claude did not run directly: %v", err)
	}
	if calls := sudo.calls(t); calls != "" {
		t.Fatalf("sudo ran without run_as_user:\n%s", calls)
	}
}
