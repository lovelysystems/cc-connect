//go:build runas && linux

package runas

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	binary      = "/usr/local/bin/cc-connect"
	fakeClaude  = "/usr/local/bin/claude"
	sudoersFile = "/etc/sudoers.d/cc-e2e"
	dataDir     = "/var/lib/cc-e2e"
	workRoot    = "/srv/cc-e2e"
	bridgePort  = 19810
	bridgeToken = "runas-e2e-token"
	neverCron   = "0 0 1 1 *"
)

var socketPath = filepath.Join(dataDir, "run", "api.sock")

// projects lists each project with its run_as_user; D has none and runs as
// cc-connect's own user.
var projects = []struct{ name, user string }{
	{"A", "agent_a"},
	{"B", "agent_b"},
	{"C", "agent_b"},
	{"D", ""},
}

// fakeClaudeScript stands in for the Claude CLI. On each user turn it calls
// cc-connect send from inside the session, then ends the turn.
const fakeClaudeScript = `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"fake-session"}'
while IFS= read -r line; do
	case "$line" in
	*'"type":"user"'*)
		cc-connect send -m "from-agent $(id -un)" >&2
		echo '{"type":"result","subtype":"success","result":"turn done","session_id":"fake-session"}'
		;;
	esac
done
`

// prepareHost builds cc-connect, installs the fake claude, creates the users
// and gives ccsup the scoped sudo rule the docs name for a non-root
// supervisor.
func prepareHost(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, root, "go", "build", "-buildvcs=false", "-tags", "no_web", "-o", binary, "./cmd/cc-connect")
	writeFile(t, fakeClaude, fakeClaudeScript, 0o755)
	for _, name := range []string{"agent_a", "agent_b", "stranger", "ccsup"} {
		if exec.Command("id", "-u", name).Run() != nil {
			mustRun(t, "/", "useradd", "-m", name)
		}
	}
	writeFile(t, sudoersFile, "ccsup ALL=(agent_a,agent_b) NOPASSWD: ALL\n", 0o440)
	mustRun(t, "/", "visudo", "-cf", sudoersFile)
}

// prepareDirs lays out fresh folders for one run: data_dir owned by the
// supervisor with mode 0711, one work_dir per project owned by the user it
// runs as, and the config folder and a 0700 folder owned by the supervisor.
func prepareDirs(t *testing.T, sup string) {
	t.Helper()
	for _, d := range []string{dataDir, workRoot} {
		if err := os.RemoveAll(d); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(t, dataDir, sup, 0o711)
	mkdir(t, workRoot, "root", 0o755)
	mkdir(t, filepath.Join(workRoot, "work"), "root", 0o755)
	for _, p := range projects {
		owner := p.user
		if owner == "" {
			owner = sup
		}
		mkdir(t, workDir(p.name), owner, 0o755)
	}
	mkdir(t, filepath.Join(workRoot, "conf"), sup, 0o700)
	mkdir(t, filepath.Join(workRoot, "locked"), sup, 0o700)
}

func workDir(project string) string {
	return filepath.Join(workRoot, "work", project)
}

// writeConfig writes a config with every project at path, owned by sup.
// Each project has a LINE platform, which starts without reaching LINE; the
// bridge, when enabled, adds the platform the test talks to.
func writeConfig(t *testing.T, sup, name, dataDir string, bridge bool) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "data_dir = %q\n\n", dataDir)
	if bridge {
		fmt.Fprintf(&b, "[bridge]\nenabled = true\nport = %d\ntoken = %q\n\n", bridgePort, bridgeToken)
	}
	for i, p := range projects {
		fmt.Fprintf(&b, "[[projects]]\nname = %q\n", p.name)
		if p.user != "" {
			fmt.Fprintf(&b, "run_as_user = %q\n", p.user)
		}
		fmt.Fprintf(&b, "\n[projects.agent]\ntype = \"claudecode\"\n\n[projects.agent.options]\nwork_dir = %q\n\n", workDir(p.name))
		fmt.Fprintf(&b, "[[projects.platforms]]\ntype = \"line\"\n\n[projects.platforms.options]\nchannel_secret = \"unused\"\nchannel_token = \"unused\"\nport = \"%d\"\n\n", 18081+i)
	}
	path := filepath.Join(workRoot, "conf", name)
	writeFile(t, path, b.String(), 0o600)
	chown(t, path, sup)
	return path
}

// instance is a running cc-connect.
type instance struct {
	cmd  *exec.Cmd
	out  *syncBuffer
	done chan struct{}
}

// startCCConnect starts cc-connect as sup and stops it when the test ends.
func startCCConnect(t *testing.T, sup, config string) *instance {
	t.Helper()
	argv := []string{binary, "-config", config}
	if sup != "root" {
		argv = append([]string{"sudo", "-u", sup, "--"}, argv...)
	}
	inst := &instance{cmd: exec.Command(argv[0], argv[1:]...), out: &syncBuffer{}, done: make(chan struct{})}
	inst.cmd.Dir = "/"
	inst.cmd.Stdout = inst.out
	inst.cmd.Stderr = inst.out
	if err := inst.cmd.Start(); err != nil {
		t.Fatalf("start cc-connect: %v", err)
	}
	go func() {
		_ = inst.cmd.Wait()
		close(inst.done)
	}()
	t.Cleanup(func() {
		_ = inst.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-inst.done:
		case <-time.After(20 * time.Second):
			_ = inst.cmd.Process.Kill()
			<-inst.done
		}
		if t.Failed() {
			t.Logf("cc-connect output:\n%s", inst.out.String())
		}
	})
	return inst
}

// waitReady waits until the API socket answers sup and the bridge accepts an
// adapter, and returns that adapter.
func (inst *instance) waitReady(t *testing.T, sup string) *bridgeClient {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-inst.done:
			t.Fatalf("cc-connect exited during startup:\n%s", inst.out.String())
		default:
		}
		if _, err := cli(sup, "cron", "list"); err == nil {
			if b, err := dialBridge(); err == nil {
				t.Cleanup(b.close)
				return b
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("cc-connect not ready after 90s:\n%s", inst.out.String())
	return nil
}

// runAs runs argv as user with CC_DATA_DIR set to the test's data_dir. root
// runs it directly; any other user through sudo -u.
func runAs(user, stdin string, argv ...string) (string, error) {
	argv = append([]string{"env", "CC_DATA_DIR=" + dataDir}, argv...)
	if user != "root" {
		argv = append([]string{"sudo", "-u", user, "--"}, argv...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = "/"
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// cli runs the cc-connect CLI as user.
func cli(user string, args ...string) (string, error) {
	return runAs(user, "", append([]string{binary}, args...)...)
}

// api calls the API socket as user and returns the status and body.
func api(t *testing.T, user, method, path, body string) (int, string) {
	t.Helper()
	out, err := runAs(user, body, "curl", "-sS", "--unix-socket", socketPath, "-X", method,
		"-H", "Content-Type: application/json", "--data-binary", "@-", "-w", "\n%{http_code}", "http://unix"+path)
	if err != nil {
		t.Fatalf("curl %s %s as %s: %v: %s", method, path, user, err, out)
	}
	i := strings.LastIndexByte(out, '\n')
	status, err := strconv.Atoi(out[i+1:])
	if err != nil {
		t.Fatalf("curl %s %s as %s: no status in %q", method, path, user, out)
	}
	return status, strings.TrimSpace(out[:i])
}

var cronIDPattern = regexp.MustCompile(`Cron job created: (\S+)`)

// addCronJob adds a cron exec job to project as user and returns its id.
func addCronJob(t *testing.T, user, project, command string, extra ...string) string {
	t.Helper()
	args := append([]string{"cron", "add", "-p", project, "-s", sessionKey(project), "-c", neverCron, "--exec", command}, extra...)
	out, err := cli(user, args...)
	m := cronIDPattern.FindStringSubmatch(out)
	if err != nil || m == nil {
		t.Fatalf("cron add in %s as %s: %v: %s", project, user, err, out)
	}
	return m[1]
}

// sessionKey is the bridge chat of project.
func sessionKey(project string) string {
	return "bridge:chat" + project + ":u1"
}

// userProcesses lists the processes of user, one per line.
func userProcesses(user string) string {
	out, _ := exec.Command("pgrep", "-a", "-u", user).Output()
	return strings.TrimSpace(string(out))
}

// waitFor polls cond every 200ms until it holds or timeout passes.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func mustRun(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(argv, " "), err, out)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path, owner string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	chown(t, path, owner)
}

func chown(t *testing.T, path, owner string) {
	t.Helper()
	u, err := user.Lookup(owner)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.Chown(path, uid, gid); err != nil {
		t.Fatal(err)
	}
}

// syncBuffer collects a process's output from its writer goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
