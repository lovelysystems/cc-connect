//go:build !windows

package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestBuildSpawnCommand_Legacy(t *testing.T) {
	ctx := context.Background()
	cmd := BuildSpawnCommand(ctx, SpawnOptions{}, "claude", "--version")
	if cmd == nil {
		t.Fatal("BuildSpawnCommand returned nil")
	}
	if cmd.Path == "" || !strings.HasSuffix(cmd.Args[0], "claude") {
		t.Fatalf("legacy spawn: want path ending in claude, got %q (args %v)", cmd.Path, cmd.Args)
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "--version" {
		t.Fatalf("legacy spawn: unexpected args %v", cmd.Args)
	}
}

func TestBuildSpawnCommand_RunAsUser(t *testing.T) {
	ctx := context.Background()
	opts := SpawnOptions{
		RunAsUser:    "partseeker-coder",
		EnvAllowlist: []string{"PGSSLROOTCERT", "PGSSLMODE"},
	}
	cmd := BuildSpawnCommand(ctx, opts, "claude", "--version", "-p", "hello")
	if cmd == nil {
		t.Fatal("BuildSpawnCommand returned nil")
	}
	if !strings.HasSuffix(cmd.Args[0], "sudo") {
		t.Fatalf("want argv[0] ending in sudo, got %q", cmd.Args[0])
	}
	// Expected: sudo -n -iu partseeker-coder --preserve-env=<allow> -- claude --version -p hello
	want := []string{"-n", "-iu", "partseeker-coder"}
	if !reflect.DeepEqual(cmd.Args[1:4], want) {
		t.Fatalf("sudo args[1:4] = %v, want %v", cmd.Args[1:4], want)
	}
	if !strings.HasPrefix(cmd.Args[4], "--preserve-env=") {
		t.Fatalf("expected --preserve-env= at args[4], got %q", cmd.Args[4])
	}
	// Allowlist must include both defaults and the extensions, sorted+deduped.
	preserveList := strings.TrimPrefix(cmd.Args[4], "--preserve-env=")
	preserved := strings.Split(preserveList, ",")
	for _, needed := range []string{"LANG", "LC_ALL", "TERM", "PGSSLROOTCERT", "PGSSLMODE"} {
		if !slices.Contains(preserved, needed) {
			t.Errorf("preserve-env missing %q; got %v", needed, preserved)
		}
	}
	// PATH must NOT be in the allowlist — sudo -i rebuilds it from the
	// target user's login profile, and preserving the supervisor's PATH
	// would leak supervisor work dirs into the isolated session.
	if slices.Contains(preserved, "PATH") {
		t.Errorf("preserve-env leaked PATH; got %v", preserved)
	}
	if cmd.Args[5] != "--" {
		t.Fatalf("args[5] = %q, want --", cmd.Args[5])
	}
	if cmd.Args[6] != "claude" {
		t.Fatalf("args[6] = %q, want claude", cmd.Args[6])
	}
	if !reflect.DeepEqual(cmd.Args[7:], []string{"--version", "-p", "hello"}) {
		t.Fatalf("agent args = %v, want [--version -p hello]", cmd.Args[7:])
	}
}

// TestBuildSpawnCommand_RunAsUserWithWorkDir guards the fix for the bug where
// sudo -i runs the command from the target user's HOME, ignoring cmd.Dir, so a
// run_as_user agent ignored its (multi-workspace) working directory. With
// WorkDir set the command must be wrapped so it chdirs into WorkDir — passed
// via the CC_RUNAS_CHDIR env var (added to the preserve-env allowlist), not
// argv, so non-ASCII paths survive sudo's command re-quoting.
func TestBuildSpawnCommand_RunAsUserWithWorkDir(t *testing.T) {
	ctx := context.Background()
	opts := SpawnOptions{
		RunAsUser: "partseeker-coder",
		WorkDir:   "/opt/shared/workspaces/设计原型界面",
	}
	cmd := BuildSpawnCommand(ctx, opts, "claude", "--version", "-p", "hello")
	if cmd == nil {
		t.Fatal("BuildSpawnCommand returned nil")
	}
	// The chdir env var must be on the preserve-env allowlist or it would be
	// stripped before the wrapper can read it.
	preserved := strings.Split(strings.TrimPrefix(cmd.Args[4], "--preserve-env="), ",")
	if !slices.Contains(preserved, RunAsChdirEnv) {
		t.Errorf("preserve-env missing %q; got %v", RunAsChdirEnv, preserved)
	}
	if cmd.Args[5] != "--" {
		t.Fatalf("args[5] = %q, want --", cmd.Args[5])
	}
	// Expected wrapper: -- /bin/sh -c "cd \"$CC_RUNAS_CHDIR\" || exit 1; exec \"$@\"" sh claude --version -p hello
	wantWrap := []string{
		"/bin/sh", "-c",
		"cd \"$" + RunAsChdirEnv + "\" || exit 1; exec \"$@\"",
		"sh", "claude", "--version", "-p", "hello",
	}
	if !reflect.DeepEqual(cmd.Args[6:], wantWrap) {
		t.Fatalf("wrapped argv = %v, want %v", cmd.Args[6:], wantWrap)
	}
	// The path itself must NOT appear in argv (it travels via env).
	for _, a := range cmd.Args {
		if strings.Contains(a, "设计原型界面") {
			t.Errorf("workdir leaked into argv %q; must travel via %s env", a, RunAsChdirEnv)
		}
	}
}

// TestMergedAllowlist_WorkDirAddsChdirEnv asserts the chdir env var is only
// allowlisted when WorkDir is set (no leak in the common case).
func TestMergedAllowlist_WorkDirAddsChdirEnv(t *testing.T) {
	withWD := SpawnOptions{RunAsUser: "u", WorkDir: "/tmp/ws"}.mergedAllowlist()
	if !slices.Contains(withWD, RunAsChdirEnv) {
		t.Errorf("with WorkDir: allowlist missing %q; got %v", RunAsChdirEnv, withWD)
	}
	noWD := SpawnOptions{RunAsUser: "u"}.mergedAllowlist()
	if slices.Contains(noWD, RunAsChdirEnv) {
		t.Errorf("without WorkDir: allowlist should not contain %q; got %v", RunAsChdirEnv, noWD)
	}
}

func TestFilterEnvForSpawn_Legacy(t *testing.T) {
	env := []string{"PATH=/usr/bin", "SECRET=top", "PWD=/tmp"}
	got := FilterEnvForSpawn(env, SpawnOptions{})
	if !reflect.DeepEqual(got, env) {
		t.Fatalf("legacy mode should pass env through unchanged; got %v, want %v", got, env)
	}
}

func TestFilterEnvForSpawn_RunAsUser(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"LANG=en_US.UTF-8",
		"SECRET=top",
		"HOME=/home/supervisor",
		"SUPERVISOR_CREDENTIAL=nope",
		"PGSSLROOTCERT=/etc/certs/root.crt",
	}
	opts := SpawnOptions{
		RunAsUser:    "target",
		EnvAllowlist: []string{"PGSSLROOTCERT"},
	}
	got := FilterEnvForSpawn(env, opts)
	// LANG, PGSSLROOTCERT should survive; PATH, SECRET, HOME,
	// SUPERVISOR_CREDENTIAL must not. PATH is deliberately dropped so
	// sudo -i can rebuild it from the target user's login profile
	// instead of inheriting the supervisor's PATH.
	wantKept := map[string]bool{
		"LANG=en_US.UTF-8":                 true,
		"PGSSLROOTCERT=/etc/certs/root.crt": true,
	}
	for _, e := range got {
		if !wantKept[e] {
			t.Errorf("unexpected env survived filter: %q", e)
		}
		delete(wantKept, e)
	}
	for e := range wantKept {
		t.Errorf("expected env missing after filter: %q", e)
	}
	for _, e := range got {
		if strings.HasPrefix(e, "PATH=") || strings.HasPrefix(e, "SECRET=") || strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "SUPERVISOR_CREDENTIAL=") {
			t.Errorf("disallowed env leaked: %q", e)
		}
	}
}

// stubSudoRunner implements SudoRunner for tests.
type stubSudoRunner struct {
	// script maps an argv slice (joined with \x1f) to a response.
	script map[string]stubResponse
	// calls records every invocation in order.
	calls [][]string
}

type stubResponse struct {
	out []byte
	err error
}

func (s *stubSudoRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	s.calls = append(s.calls, append([]string{}, args...))
	key := strings.Join(args, "\x1f")
	if r, ok := s.script[key]; ok {
		return r.out, r.err
	}
	return nil, errors.New("stubSudoRunner: unscripted call: " + strings.Join(args, " "))
}

func key(args ...string) string { return strings.Join(args, "\x1f") }

func TestVerifyRunAsUserCheap_Success(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/usr/bin/true"):                           {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/usr/bin/true"):             {[]byte("a password is required"), &exec.ExitError{}},
		},
	}
	if err := VerifyRunAsUserCheap(context.Background(), runner, "target"); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("want 2 sudo calls, got %d", len(runner.calls))
	}
}

func TestVerifyRunAsUserCheap_NoPasswordlessSudoToTarget(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/usr/bin/true"): {[]byte("a password is required"), &exec.ExitError{}},
		},
	}
	err := VerifyRunAsUserCheap(context.Background(), runner, "target")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "passwordless sudo to user") {
		t.Errorf("error = %v, want 'passwordless sudo to user' message", err)
	}
}

func TestVerifyRunAsUserCheap_TargetCanEscalate(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/usr/bin/true"):               {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/usr/bin/true"): {nil, nil}, // BAD — escalation succeeded
		},
	}
	err := VerifyRunAsUserCheap(context.Background(), runner, "target")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "can run passwordless sudo") {
		t.Errorf("error = %v, want 'can run passwordless sudo' message", err)
	}
}

func TestVerifyRunAsUserCheap_EmptyUser(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{script: map[string]stubResponse{}}
	if err := VerifyRunAsUserCheap(context.Background(), runner, ""); err == nil {
		t.Fatal("want error for empty user")
	}
}

func TestVerifyRunAsUserCheap_CacheHit(t *testing.T) {
	ResetVerifyCache()
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/usr/bin/true"):               {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/usr/bin/true"): {nil, &exec.ExitError{}},
		},
	}
	// First call populates the cache with 2 runner calls.
	if err := VerifyRunAsUserCheap(context.Background(), runner, "target"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("first call: want 2 runner calls, got %d", len(runner.calls))
	}
	// Second call should be served from the cache — zero new runner calls.
	if err := VerifyRunAsUserCheap(context.Background(), runner, "target"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("cached call made runner calls; want 2 total, got %d", len(runner.calls))
	}
}

// fakeSudoScript stands in for sudo on PATH. It records its argv, fails the
// target's own nested `sudo -n` the way a correctly scoped sudoers does, and
// in exec mode runs the command the way `sudo -n -iu USER --` does: every
// byte of every argument except alphanumerics, '_', '-' and '$' is
// backslash-escaped, the arguments are joined into one string and that
// string runs under `sh -c` in the target's HOME with only the preserved
// variables set. In record mode it runs nothing; in fail mode it refuses
// every call; in exists mode every write exits with the "exists" code. In
// noisy mode it stays the parent the way sudo does, passes TERM on to the
// login shell, and prints on stdout and stderr before and after the command;
// that login shell runs one more command after the given one, so it does not
// replace itself with it.
// Quoting mistakes show only when /bin/sh is dash (Linux CI): macOS bash 3.2
// does not split an expanded "$NAME" in a word that also holds ${...} or "$@".
const fakeSudoScript = `#!/bin/sh
log=@LOG@
mode=@MODE@
home=@HOME@
innerpath=@PATH@
printf '%s\n' "$*" >> "$log"
if [ "$mode" = fail ]; then
	echo "sudo: a password is required" >&2
	exit 1
fi
user=
preserve=
while [ $# -gt 0 ]; do
	case "$1" in
	-iu) user=$2; shift 2 ;;
	--preserve-env=*) preserve=${1#--preserve-env=}; shift ;;
	--) shift; break ;;
	*) shift ;;
	esac
done
if [ "$1" = sudo ]; then
	exit 1
fi
if [ "$mode" = exists ] && [ "$1" = /bin/sh ]; then
	exit 17
fi
if [ "$mode" = record ]; then
	exit 0
fi
joined=
for a in "$@"; do
	joined="$joined $(printf '%s' "$a" | sed -e 's/[^A-Za-z0-9_$-]/\\&/g' -e '$!s/$/\\/')"
done
set -- HOME="$home" PATH="$innerpath" CC_FAKE_SUDO_USER="$user"
oldifs=$IFS
IFS=,
for v in $preserve; do
	eval "isset=\${$v+x}"
	if [ -n "$isset" ]; then
		eval "val=\$$v"
		set -- "$@" "$v=$val"
	fi
done
IFS=$oldifs
echo "fake login profile noise"
cd "$home" || exit 1
if [ "$mode" != noisy ]; then
	exec env -i "$@" /bin/sh -c "$joined"
fi
echo "fake login profile noise on stderr" >&2
exec 3<&0
env -i "$@" /bin/sh -c "$joined; :" <&3 &
child=$!
trap 'kill -TERM "$child" 2>/dev/null' TERM
while :; do
	wait "$child"
	s=$?
	kill -0 "$child" 2>/dev/null || break
done
echo "fake logout noise"
echo "fake logout noise on stderr" >&2
exit "$s"
`

type fakeSudo struct {
	log  string
	home string
}

// installFakeSudo puts fakeSudoScript first on PATH in the given mode
// ("exec", "record", "fail", "exists" or "noisy").
func installFakeSudo(t *testing.T, mode string) *fakeSudo {
	t.Helper()
	return installFakeSudoWithPath(t, mode, "/usr/bin:/bin")
}

// installFakeSudoWithPath is installFakeSudo with innerPath as the PATH the
// command gets.
func installFakeSudoWithPath(t *testing.T, mode, innerPath string) *fakeSudo {
	t.Helper()
	binDir := t.TempDir()
	f := &fakeSudo{log: filepath.Join(binDir, "sudo.log"), home: t.TempDir()}
	script := strings.NewReplacer(
		"@LOG@", shellQuote(f.log),
		"@MODE@", mode,
		"@HOME@", shellQuote(f.home),
		"@PATH@", shellQuote(innerPath),
	).Replace(fakeSudoScript)
	if err := os.WriteFile(filepath.Join(binDir, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	ResetVerifyCache()
	t.Cleanup(ResetVerifyCache)
	return f
}

func (f *fakeSudo) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// sudo -i leaves '$' unescaped, so a script that read "$NAME" would have the
// login shell expand it outside quotes and split the path on its spaces.
func TestWriteFileAsUser_PathWithSpaces(t *testing.T) {
	installFakeSudo(t, "exec")
	path := filepath.Join(t.TempDir(), "dir with  spaces", "file name.txt")

	if err := WriteFileAsUser(context.Background(), SpawnOptions{RunAsUser: "target"}, path, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "hi" {
		t.Fatalf("content = %q, err = %v", got, err)
	}
}

func TestSaveFilesToDiskAs_RunAsUserWritesBytesThroughSudo(t *testing.T) {
	sudo := installFakeSudo(t, "exec")
	workDir := t.TempDir()
	data := []byte{0x00, 0xff, '\n', '$', 'x', '\'', '"', 0x7f}

	paths := SaveFilesToDiskAs(context.Background(), SpawnOptions{RunAsUser: "target"}, workDir, "msg-1",
		[]FileAttachment{{FileName: "blob.bin", Data: data}})

	want := filepath.Join(workDir, ".cc-connect", "attachments", "msg-1", "blob.bin")
	if !slices.Equal(paths, []string{want}) {
		t.Fatalf("paths = %v, want [%s]", paths, want)
	}
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q, want %q", got, data)
	}
	if !strings.Contains(sudo.calls(t), "-n -iu target --preserve-env=") || !strings.Contains(sudo.calls(t), runAsWritePathEnv) {
		t.Fatalf("write did not go through sudo -n -iu target; sudo log:\n%s", sudo.calls(t))
	}
}

func TestSaveFilesToDiskAs_RunAsUserSuffixesExistingName(t *testing.T) {
	installFakeSudo(t, "exec")
	workDir := t.TempDir()
	opts := SpawnOptions{RunAsUser: "target"}

	first := SaveFilesToDiskAs(context.Background(), opts, workDir, "msg-1",
		[]FileAttachment{{FileName: "name.txt", Data: []byte("one")}})
	second := SaveFilesToDiskAs(context.Background(), opts, workDir, "msg-1",
		[]FileAttachment{{FileName: "name.txt", Data: []byte("two")}})

	dir := filepath.Join(workDir, ".cc-connect", "attachments", "msg-1")
	if !slices.Equal(first, []string{filepath.Join(dir, "name.txt")}) {
		t.Fatalf("first paths = %v", first)
	}
	if !slices.Equal(second, []string{filepath.Join(dir, "name_1.txt")}) {
		t.Fatalf("second paths = %v", second)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "name.txt")); string(got) != "one" {
		t.Fatalf("name.txt = %q, want one", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "name_1.txt")); string(got) != "two" {
		t.Fatalf("name_1.txt = %q, want two", got)
	}
}

func TestSaveFilesToDiskAs_RunAsUserFlatLayoutRefusesExistingName(t *testing.T) {
	installFakeSudo(t, "exec")
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".cc-connect", "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "name.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths := SaveFilesToDiskAs(context.Background(), SpawnOptions{RunAsUser: "target"}, workDir, "",
		[]FileAttachment{{FileName: "name.txt", Data: []byte("new")}})

	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none", paths)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "name.txt")); string(got) != "old" {
		t.Fatalf("name.txt = %q, want old", got)
	}
}

func TestSaveFilesToDiskAs_RunAsUserNeverFollowsPlantedLinkAsSupervisor(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, workDir, outside string)
	}{
		{"cc-connect dir is a link", func(t *testing.T, workDir, outside string) {
			if err := os.Symlink(outside, filepath.Join(workDir, ".cc-connect")); err != nil {
				t.Fatal(err)
			}
		}},
		{"attachments dir is a link", func(t *testing.T, workDir, outside string) {
			if err := os.MkdirAll(filepath.Join(workDir, ".cc-connect"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(workDir, ".cc-connect", "attachments")); err != nil {
				t.Fatal(err)
			}
		}},
		{"destination file is a link", func(t *testing.T, workDir, outside string) {
			dir := filepath.Join(workDir, ".cc-connect", "attachments", "msg-1")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "victim"), filepath.Join(dir, "name.txt")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installFakeSudo(t, "record")
			workDir := t.TempDir()
			outside := t.TempDir()
			tc.plant(t, workDir, outside)

			SaveFilesToDiskAs(context.Background(), SpawnOptions{RunAsUser: "target"}, workDir, "msg-1",
				[]FileAttachment{{FileName: "name.txt", Data: []byte("x")}})

			if names := dirEntries(t, outside); len(names) != 0 {
				t.Fatalf("supervisor wrote through the planted link: %v", names)
			}
		})
	}
}

func TestSaveFilesToDiskAs_RunAsUserRefusesDanglingLinkAtDestination(t *testing.T) {
	cases := []struct {
		name      string
		messageID string
		wantPath  string
	}{
		{"flat layout", "", ""},
		{"scoped layout", "msg-1", "name_1.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installFakeSudo(t, "exec")
			workDir := t.TempDir()
			outside := t.TempDir()
			dir := filepath.Join(workDir, ".cc-connect", "attachments", tc.messageID)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "name.txt")
			if err := os.Symlink(filepath.Join(outside, "victim"), link); err != nil {
				t.Fatal(err)
			}

			paths := SaveFilesToDiskAs(context.Background(), SpawnOptions{RunAsUser: "target"}, workDir, tc.messageID,
				[]FileAttachment{{FileName: "name.txt", Data: []byte("x")}})

			var want []string
			if tc.wantPath != "" {
				want = []string{filepath.Join(dir, tc.wantPath)}
			}
			if !slices.Equal(paths, want) {
				t.Fatalf("paths = %v, want %v", paths, want)
			}
			if names := dirEntries(t, outside); len(names) != 0 {
				t.Fatalf("write followed the dangling link: %v", names)
			}
			if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("planted link was replaced (err %v)", err)
			}
		})
	}
}

func TestSaveFilesToDiskAs_RunAsUserFakedExistsStopsSuffixing(t *testing.T) {
	sudo := installFakeSudo(t, "exists")
	workDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	paths := SaveFilesToDiskAs(ctx, SpawnOptions{RunAsUser: "target"}, workDir, "msg-1",
		[]FileAttachment{{FileName: "name.txt", Data: []byte("x")}})

	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none", paths)
	}
	if ctx.Err() != nil {
		t.Fatal("suffix loop ran until the deadline")
	}
	if n := strings.Count(sudo.calls(t), "/bin/sh -c"); n != maxAsUserSuffix+1 {
		t.Fatalf("write attempts = %d, want %d", n, maxAsUserSuffix+1)
	}
}

func TestSaveFilesToDiskAs_RunAsUserSudoFailureWritesNothing(t *testing.T) {
	installFakeSudo(t, "fail")
	workDir := t.TempDir()

	paths := SaveFilesToDiskAs(context.Background(), SpawnOptions{RunAsUser: "target"}, workDir, "msg-1",
		[]FileAttachment{{FileName: "name.txt", Data: []byte("x")}})

	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none", paths)
	}
	if names := dirEntries(t, workDir); len(names) != 0 {
		t.Fatalf("work dir = %v, want empty", names)
	}
}

func TestSaveFilesToDiskAs_RunAsUserUnwritableDirSkipsPromptly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only dir")
	}
	installFakeSudo(t, "exec")
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".cc-connect", "attachments", "msg-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	paths := SaveFilesToDiskAs(ctx, SpawnOptions{RunAsUser: "target"}, workDir, "msg-1",
		[]FileAttachment{{FileName: "name.txt", Data: []byte("x")}})

	if len(paths) != 0 {
		t.Fatalf("paths = %v, want none", paths)
	}
	if ctx.Err() != nil {
		t.Fatal("write kept retrying until the deadline")
	}
}

func TestSaveFilesToDiskAs_WithoutRunAsUserSkipsSudo(t *testing.T) {
	sudo := installFakeSudo(t, "record")
	workDir := t.TempDir()

	paths := SaveFilesToDiskAs(context.Background(), SpawnOptions{}, workDir, "msg-1",
		[]FileAttachment{{FileName: "name.txt", Data: []byte("x")}})

	want := filepath.Join(workDir, ".cc-connect", "attachments", "msg-1", "name.txt")
	if !slices.Equal(paths, []string{want}) {
		t.Fatalf("paths = %v, want [%s]", paths, want)
	}
	if calls := sudo.calls(t); calls != "" {
		t.Fatalf("sudo ran without run_as_user:\n%s", calls)
	}
}

// runProjectCmd builds c as opts' user, runs it with stdin and returns what
// each stream printed inside its frame.
func runProjectCmd(t *testing.T, ctx context.Context, opts SpawnOptions, c projectCommand, stdin string) (stdout, stderr string, err error) {
	t.Helper()
	cmd, err := buildProjectCommand(ctx, opts, c)
	if err != nil {
		return "", "", err
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return string(projectCommandOutput(outBuf.Bytes())), string(projectCommandOutput(errBuf.Bytes())), err
}

func shCommand(command, dir string, env ...string) projectCommand {
	return projectCommand{shell: "sh", flag: "-c", command: command, dir: dir, env: env}
}

// waitGone fails the test unless the process whose pid is in pidFile has
// ended within a few seconds.
func waitGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("pid file %q: %v", data, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("process %d still runs after cancel", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never written", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBuildProjectCommand_WithoutRunAsIsShellExecCommand(t *testing.T) {
	ctx := context.Background()
	c := projectCommand{shell: "bash", flag: "-lc", profile: "source ~/.bashrc;", command: "echo $HOME", dir: "/tmp/w", env: []string{"CC_PROJECT=p"}}
	got, err := buildProjectCommand(ctx, SpawnOptions{}, c)
	if err != nil {
		t.Fatal(err)
	}
	want := shellExecCommand(ctx, "bash", "-lc", "source ~/.bashrc;", "echo $HOME")
	if !reflect.DeepEqual(got.Args, want.Args) || got.Path != want.Path {
		t.Fatalf("argv = %q, want %q", got.Args, want.Args)
	}
	if got.Dir != "/tmp/w" || got.Env != nil || got.WaitDelay != 0 {
		t.Fatalf("dir %q env %v waitDelay %v; want /tmp/w, nil, 0", got.Dir, got.Env, got.WaitDelay)
	}
}

func TestBuildProjectCommand_RunAsArgvKeepsCommandOutOfArgv(t *testing.T) {
	installFakeSudo(t, "exec")
	opts := SpawnOptions{RunAsUser: "target", EnvAllowlist: []string{"PGHOST"}}
	cmd, err := buildProjectCommand(context.Background(), opts, shCommand("echo secret-word", "/w", "CC_PROJECT=p"))
	if err != nil {
		t.Fatal(err)
	}
	args := cmd.Args
	if filepath.Base(args[0]) != "sudo" || !reflect.DeepEqual(args[1:4], []string{"-n", "-iu", "target"}) {
		t.Fatalf("argv = %q", args)
	}
	preserved := strings.Split(strings.TrimPrefix(args[4], "--preserve-env="), ",")
	if !slices.IsSorted(preserved) {
		t.Fatalf("preserve list not sorted: %v", preserved)
	}
	for _, name := range []string{"LANG", "PGHOST", "CC_PROJECT", "CC_RUNAS_CMD", "CC_RUNAS_CMD_DIR"} {
		if !slices.Contains(preserved, name) {
			t.Errorf("preserve list misses %s: %v", name, preserved)
		}
	}
	if !reflect.DeepEqual(args[5:9], []string{"--", "exec", "/bin/sh", "-c"}) || len(args) != 10 {
		t.Fatalf("argv tail = %q", args[5:])
	}
	if strings.Contains(strings.Join(args, " "), "secret-word") {
		t.Fatalf("command text in argv: %q", args)
	}
	if cmd.Dir != "" {
		t.Fatalf("Dir = %q, want empty", cmd.Dir)
	}
}

func TestBuildProjectCommand_RunAsCommandReachesShellUnexpanded(t *testing.T) {
	installFakeSudo(t, "exec")
	stdout, _, err := runProjectCmd(t, context.Background(), SpawnOptions{RunAsUser: "target"},
		shCommand(`printf '%s\n' '$HOME  and ${PATH} "q"'`, ""), "")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "$HOME  and ${PATH} \"q\"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestBuildProjectCommand_RunAsDropsLoginNoise(t *testing.T) {
	installFakeSudo(t, "noisy")
	opts := SpawnOptions{RunAsUser: "target"}
	cmd, err := buildProjectCommand(context.Background(), opts, shCommand("echo out; echo err >&2; printf last", ""))
	if err != nil {
		t.Fatal(err)
	}
	var outBuf, errBuf bytes.Buffer
	var mu sync.Mutex
	var lines []string
	add := func(s string) { mu.Lock(); lines = append(lines, s); mu.Unlock() }
	outLines := newCommandLineWriter(true, func(s string) { add("out:" + s) })
	errLines := newCommandLineWriter(true, func(s string) { add("err:" + s) })
	cmd.Stdout = io.MultiWriter(&outBuf, outLines)
	cmd.Stderr = io.MultiWriter(&errBuf, errLines)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	outLines.flush()
	errLines.flush()
	if !strings.Contains(outBuf.String(), "fake logout noise") || !strings.Contains(errBuf.String(), "noise on stderr") {
		t.Fatalf("fake sudo printed no noise: %q / %q", outBuf.String(), errBuf.String())
	}
	if got := string(projectCommandOutput(outBuf.Bytes())); got != "out\nlast" {
		t.Errorf("stdout = %q", got)
	}
	if got := string(projectCommandOutput(errBuf.Bytes())); got != "err\n" {
		t.Errorf("stderr = %q", got)
	}
	slices.Sort(lines)
	if want := []string{"err:err", "out:last", "out:out"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
}

func TestBuildProjectCommand_RunAsEnvIsAllowlistOnly(t *testing.T) {
	installFakeSudo(t, "exec")
	t.Setenv("CC_TEST_SECRET", "s3cret")
	t.Setenv("PGHOST", "db")
	stdout, _, err := runProjectCmd(t, context.Background(),
		SpawnOptions{RunAsUser: "target", EnvAllowlist: []string{"PGHOST"}},
		shCommand("env", "", "CC_PROJECT=proj", "CC_SESSION_KEY=sk"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PGHOST=db", "CC_PROJECT=proj", "CC_SESSION_KEY=sk", "CC_FAKE_SUDO_USER=target"} {
		if !strings.Contains(stdout, want+"\n") {
			t.Errorf("env misses %s:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "s3cret") {
		t.Errorf("secret crossed sudo:\n%s", stdout)
	}
}

func TestBuildProjectCommand_FailedSudoCheckRunsNothing(t *testing.T) {
	sudo := installFakeSudo(t, "fail")
	_, err := buildProjectCommand(context.Background(), SpawnOptions{RunAsUser: "target"}, shCommand("echo hi", ""))
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(sudo.calls(t), "/bin/sh") {
		t.Fatalf("command ran after a failed check: %s", sudo.calls(t))
	}
}

func TestBuildProjectCommand_RunAsCancelStopsCommand(t *testing.T) {
	for _, tc := range []struct{ name, command string }{
		{"background child", `sleep 61 & echo "${!}" > "$PIDFILE"; wait`},
		{"ignores TERM", `trap '' TERM; echo "$$" > "$PIDFILE"; sleep 62; sleep 62`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeSudo(t, "noisy")
			pidFile := filepath.Join(t.TempDir(), "pid")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd, err := buildProjectCommand(ctx, SpawnOptions{RunAsUser: "target"}, shCommand(tc.command, "", "PIDFILE="+pidFile))
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitForFile(t, pidFile)
			start := time.Now()
			cancel()
			_ = cmd.Wait()
			if d := time.Since(start); d > cmd.WaitDelay+time.Second {
				t.Errorf("Wait took %s after cancel", d)
			}
			waitGone(t, pidFile)
		})
	}
}

func TestBuildProjectCommand_RunAsStdinAndFolder(t *testing.T) {
	sudo := installFakeSudo(t, "exec")
	opts := SpawnOptions{RunAsUser: "target"}
	dir := filepath.Join(t.TempDir(), "work  dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	in := "{\"request_id\":\"r\"}\nline two $X\n"
	stdout, _, err := runProjectCmd(t, context.Background(), opts, shCommand("pwd -P; cat", dir), in)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.EvalSymlinks(dir)
	if stdout != wantDir+"\n"+in {
		t.Fatalf("stdout = %q, want %q", stdout, wantDir+"\n"+in)
	}

	stdout, _, err = runProjectCmd(t, context.Background(), opts, shCommand("pwd -P", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.EvalSymlinks(sudo.home)
	if got, _ := filepath.EvalSymlinks(strings.TrimSpace(stdout)); got != wantHome {
		t.Fatalf("pwd with no folder = %q, want home %q", stdout, wantHome)
	}
}
