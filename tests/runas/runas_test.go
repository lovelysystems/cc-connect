//go:build runas && linux

// Package runas runs cc-connect with run_as_user projects on a real Linux
// host: real users, sudo, and the shared API socket, driven through the real
// CLI as each user. It needs root and changes the host (users, a sudoers
// file, /var/lib/cc-e2e, /srv/cc-e2e, /usr/local/bin), so run it in a
// throwaway container or CI runner.
package runas

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// missingID is a cron job id no job has.
const missingID = "00000000"

func TestRunAsUserProjects(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: creates users, a sudoers rule and folders under /var/lib and /srv")
	}
	prepareHost(t)
	t.Run("cc-connect runs as root", func(t *testing.T) { runScenario(t, "root") })
	t.Run("cc-connect runs as a non-root supervisor", func(t *testing.T) { runScenario(t, "ccsup") })
}

// runScenario starts cc-connect as sup with projects A (agent_a), B and C
// (agent_b) and D (no run_as_user), and checks what each user may do.
func runScenario(t *testing.T, sup string) {
	prepareDirs(t, sup)
	inst := startCCConnect(t, sup, writeConfig(t, sup, "config.toml", dataDir, true))
	bridge := inst.waitReady(t, sup)

	jobs := map[string]string{}
	for _, p := range projects {
		jobs[p.name] = addCronJob(t, sup, p.name, "id -un > whoami.txt")
	}
	owners := []string{"root"}
	if sup != "root" {
		owners = append(owners, sup)
	}

	t.Run("cron exec runs as the project's user", func(t *testing.T) {
		for _, c := range []struct{ project, want string }{{"A", "agent_a"}, {"D", sup}} {
			if out, err := cli(sup, "cron", "exec", jobs[c.project]); err != nil {
				t.Fatalf("cron exec in %s: %v: %s", c.project, err, out)
			}
			file := filepath.Join(workDir(c.project), "whoami.txt")
			var got string
			waitFor(20*time.Second, func() bool {
				b, _ := os.ReadFile(file)
				got = strings.TrimSpace(string(b))
				return got != ""
			})
			if got != c.want {
				t.Fatalf("cron exec in %s ran as %q, want %q", c.project, got, c.want)
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
		}
	})

	timedOut := startTimedOutJobs(t, sup)

	t.Run("send", func(t *testing.T) { runCLICases(t, sendCases(owners)) })
	t.Run("send with no project lands in the caller's only project", func(t *testing.T) {
		if out, err := cli("agent_a", "send", "-s", sessionKey("A"), "-m", "no-project-agent_a"); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if got := replyProject(t, bridge.waitReply(t, "no-project-agent_a")); got != "A" {
			t.Fatalf("message landed in project %q, want A", got)
		}
	})
	t.Run("cron add and timer add", func(t *testing.T) { runCLICases(t, addCases(owners)) })
	t.Run("cron list", func(t *testing.T) { checkCronList(t, owners, jobs) })
	t.Run("another project's job answers like a missing id", func(t *testing.T) { checkHiddenJobs(t, jobs) })
	t.Run("cron edit cannot move a job to another project", func(t *testing.T) { checkCronEdit(t, jobs) })
	t.Run("raw API routes", func(t *testing.T) { runAPICases(t, apiCases(owners)) })

	t.Run("timed-out cron exec leaves no process of the project's user", func(t *testing.T) {
		checkTimedOut(t, timedOut)
	})
	t.Run("a job refused to a caller never ran", func(t *testing.T) {
		for _, p := range projects {
			if _, err := os.Stat(filepath.Join(workDir(p.name), "whoami.txt")); err == nil {
				t.Errorf("the job of %s ran", p.name)
			}
		}
	})

	t.Run("cc-connect send from inside an agent session reaches the socket", func(t *testing.T) {
		for _, p := range []struct{ project, user string }{{"A", "agent_a"}, {"B", "agent_b"}} {
			err := bridge.send(map[string]any{
				"type": "message", "msg_id": "m-" + p.project, "session_key": sessionKey(p.project),
				"user_id": "u1", "content": "hello", "reply_ctx": "rc-" + p.project, "project": p.project,
			})
			if err != nil {
				t.Fatal(err)
			}
			bridge.waitReply(t, "from-agent "+p.user)
		}
	})
	t.Run("signoff for another project's live session answers like a missing one", func(t *testing.T) {
		runAPICases(t, []apiCase{
			{"agent_a posts into B", "agent_a", "POST", "/signoff/send", signoffBody(sessionKey("B")), 404},
			{"agent_b posts into A", "agent_b", "POST", "/signoff/send", signoffBody(sessionKey("A")), 404},
			{"stranger posts into A", "stranger", "POST", "/signoff/send", signoffBody(sessionKey("A")), 403},
		})
	})

	t.Run("a data_dir its agent users cannot enter stops startup", func(t *testing.T) {
		locked := filepath.Join(workRoot, "locked")
		config := writeConfig(t, sup, "locked.toml", filepath.Join(locked, "data"), false)
		argv := []string{binary, "-config", config}
		if sup != "root" {
			argv = append([]string{"sudo", "-u", sup, "--"}, argv...)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = "/"
		out, err := cmd.CombinedOutput()
		if err == nil || ctx.Err() != nil {
			t.Fatalf("startup did not stop: %v\n%s", err, out)
		}
		for _, want := range []string{"cannot enter " + locked, "data_dir"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("startup output lacks %q:\n%s", want, out)
			}
		}
	})
}

// cliCase runs the CLI as a user. An empty want expects success; otherwise
// the command must fail with want in its output.
type cliCase struct {
	name string
	as   string
	args []string
	want string
}

func runCLICases(t *testing.T, cases []cliCase) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := cli(c.as, c.args...)
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("%v: %s", err, out)
			case c.want != "" && (err == nil || !strings.Contains(out, c.want)):
				t.Fatalf("want failure with %q, got %v: %s", c.want, err, out)
			}
		})
	}
}

func sendArgs(project string) []string {
	args := []string{"send", "-s", sessionKey(project), "-m", "hi"}
	if project != "" {
		args = append(args, "-p", project)
	}
	return args
}

func sendCases(owners []string) []cliCase {
	var cases []cliCase
	for _, u := range owners {
		cases = append(cases,
			cliCase{u + " sends to D", u, sendArgs("D"), ""},
			cliCase{u + " sends with no project", u, []string{"send", "-s", sessionKey("D"), "-m", "hi"}, "project is required"},
			cliCase{u + " sends to an unknown project", u, sendArgs("nope"), `project "nope" not found`},
		)
	}
	return append(cases,
		cliCase{"agent_a sends to A", "agent_a", sendArgs("A"), ""},
		cliCase{"agent_a sends to B", "agent_a", sendArgs("B"), "forbidden"},
		cliCase{"agent_a sends to an unknown project", "agent_a", sendArgs("nope"), "forbidden"},
		cliCase{"agent_b sends to B", "agent_b", sendArgs("B"), ""},
		cliCase{"agent_b sends to C", "agent_b", sendArgs("C"), ""},
		cliCase{"agent_b sends to A", "agent_b", sendArgs("A"), "forbidden"},
		cliCase{"agent_b sends with no project", "agent_b", []string{"send", "-s", sessionKey("B"), "-m", "hi"}, "project is required"},
		cliCase{"stranger sends to A", "stranger", sendArgs("A"), "forbidden"},
		cliCase{"stranger sends with no project", "stranger", []string{"send", "-s", sessionKey("A"), "-m", "hi"}, "forbidden"},
	)
}

func addCases(owners []string) []cliCase {
	cronAdd := func(project string) []string {
		return []string{"cron", "add", "-p", project, "-s", sessionKey(project), "-c", neverCron, "--exec", "true"}
	}
	timerAdd := func(project string) []string {
		return []string{"timer", "add", "-p", project, "-s", sessionKey(project), "--delay", "10h", "--exec", "true"}
	}
	var cases []cliCase
	for _, add := range []struct {
		name string
		args func(string) []string
	}{{"cron add", cronAdd}, {"timer add", timerAdd}} {
		for _, u := range owners {
			cases = append(cases, cliCase{u + " " + add.name + " in D", u, add.args("D"), ""})
		}
		cases = append(cases,
			cliCase{"agent_a " + add.name + " in A", "agent_a", add.args("A"), ""},
			cliCase{"agent_a " + add.name + " in B", "agent_a", add.args("B"), "forbidden"},
			cliCase{"agent_b " + add.name + " in C", "agent_b", add.args("C"), ""},
			cliCase{"agent_b " + add.name + " in A", "agent_b", add.args("A"), "forbidden"},
			cliCase{"stranger " + add.name + " in A", "stranger", add.args("A"), "forbidden"},
		)
	}
	return cases
}

// checkCronList checks that a list without a project shows each caller the
// seeded jobs of its own projects only, and that naming another project is
// refused.
func checkCronList(t *testing.T, owners []string, jobs map[string]string) {
	type listCase struct {
		as   string
		sees []string
	}
	cases := []listCase{{"agent_a", []string{"A"}}, {"agent_b", []string{"B", "C"}}}
	for _, u := range owners {
		cases = append(cases, listCase{u, []string{"A", "B", "C", "D"}})
	}
	for _, c := range cases {
		t.Run(c.as+" lists its projects' jobs", func(t *testing.T) {
			out, err := cli(c.as, "cron", "list")
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			for _, p := range projects {
				if strings.Contains(out, jobs[p.name]) != slices.Contains(c.sees, p.name) {
					t.Errorf("job of %s listed: %v, want %v\n%s", p.name, !slices.Contains(c.sees, p.name), slices.Contains(c.sees, p.name), out)
				}
			}
		})
	}
	runCLICases(t, []cliCase{
		{"agent_a lists B", "agent_a", []string{"cron", "list", "-p", "B"}, "forbidden"},
		{"agent_b lists A", "agent_b", []string{"cron", "list", "-p", "A"}, "forbidden"},
		{"stranger lists", "stranger", []string{"cron", "list"}, "forbidden"},
	})
}

// checkHiddenJobs checks that a job of a project the caller may not act for
// gets, from cron info, exec, del and the /cron/run route, exactly the
// answer of a missing id.
func checkHiddenJobs(t *testing.T, jobs map[string]string) {
	for _, c := range []struct{ as, project string }{
		{"agent_a", "B"}, {"agent_a", "D"}, {"agent_b", "A"}, {"agent_b", "D"},
	} {
		id := jobs[c.project]
		for _, sub := range []string{"info", "exec", "del"} {
			t.Run(fmt.Sprintf("%s cron %s of %s's job", c.as, sub, c.project), func(t *testing.T) {
				hidden, hiddenErr := cli(c.as, "cron", sub, id)
				missing, missingErr := cli(c.as, "cron", sub, missingID)
				if hiddenErr == nil || missingErr == nil {
					t.Fatalf("want both to fail: %v %q, %v %q", hiddenErr, hidden, missingErr, missing)
				}
				if got := strings.ReplaceAll(hidden, id, missingID); got != missing {
					t.Fatalf("answer %q differs from the missing id's %q", got, missing)
				}
			})
		}
		t.Run(fmt.Sprintf("%s /cron/run of %s's job", c.as, c.project), func(t *testing.T) {
			hiddenStatus, hidden := api(t, c.as, "POST", "/cron/run", fmt.Sprintf(`{"id":%q}`, id))
			missingStatus, missing := api(t, c.as, "POST", "/cron/run", fmt.Sprintf(`{"id":%q}`, missingID))
			if got := strings.ReplaceAll(hidden, id, missingID); hiddenStatus != missingStatus || got != missing {
				t.Fatalf("answer %d %q differs from the missing id's %d %q", hiddenStatus, got, missingStatus, missing)
			}
		})
	}
	runCLICases(t, []cliCase{
		{"stranger cron info", "stranger", []string{"cron", "info", jobs["A"]}, "forbidden"},
		{"stranger cron del", "stranger", []string{"cron", "del", jobs["A"]}, "forbidden"},
		{"stranger cron exec", "stranger", []string{"cron", "exec", jobs["A"]}, "forbidden"},
	})
}

// checkCronEdit checks that a caller cannot move its job to another project
// and that the job stays where it was.
func checkCronEdit(t *testing.T, jobs map[string]string) {
	runCLICases(t, []cliCase{
		{"agent_a moves A's job to B", "agent_a", []string{"cron", "edit", jobs["A"], "project", "B"}, "forbidden"},
		{"agent_b moves B's job to A", "agent_b", []string{"cron", "edit", jobs["B"], "project", "A"}, "forbidden"},
		{"stranger edits A's job", "stranger", []string{"cron", "edit", jobs["A"], "description", "x"}, "forbidden"},
	})
	for _, p := range []string{"A", "B"} {
		status, body := api(t, "root", "GET", "/cron/info?id="+jobs[p], "")
		if status != 200 || !strings.Contains(body, `"project":"`+p+`"`) {
			t.Errorf("job of %s after the refused edits: %d %s", p, status, body)
		}
	}
}

// apiCase calls the API socket as a user and expects status; a 403 must
// carry no more than "forbidden".
type apiCase struct {
	name         string
	as           string
	method, path string
	body         string
	status       int
}

func runAPICases(t *testing.T, cases []apiCase) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, body := api(t, c.as, c.method, c.path, c.body)
			if status != c.status || (status == 403 && body != "forbidden") {
				t.Fatalf("got %d %q, want %d", status, body, c.status)
			}
		})
	}
}

func apiCases(owners []string) []apiCase {
	const bind = `{"platform":"bridge","chat_id":"chatR","bots":{"B":"bot-b","C":"bot-c"}}`
	relay := func(from string) string {
		return fmt.Sprintf(`{"from":%q,"to":"C","session_key":"bridge:chatR:u1","message":"hi"}`, from)
	}
	var cases []apiCase
	for _, u := range owners {
		cases = append(cases,
			apiCase{u + " lists sessions", u, "GET", "/sessions", "", 200},
			apiCase{u + " binds a relay chat", u, "POST", "/relay/bind", bind, 200},
			apiCase{u + " reads a relay binding", u, "GET", "/relay/binding?chat_id=chatR", "", 200},
		)
	}
	for _, u := range []string{"agent_a", "agent_b", "stranger"} {
		cases = append(cases,
			apiCase{u + " lists sessions", u, "GET", "/sessions", "", 403},
			apiCase{u + " binds a relay chat", u, "POST", "/relay/bind", bind, 403},
			apiCase{u + " reads a relay binding", u, "GET", "/relay/binding?chat_id=chatR", "", 403},
		)
	}
	send := func(field string) string {
		return fmt.Sprintf(`{"project":"A","session_key":%q,"message":"hi",%q:"/"}`, sessionKey("A"), field)
	}
	return append(cases,
		apiCase{"agent_a sends to A with work_dir", "agent_a", "POST", "/send", send("work_dir"), 403},
		apiCase{"agent_a sends to A with cwd", "agent_a", "POST", "/send", send("cwd"), 403},
		apiCase{"agent_a relays from B", "agent_a", "POST", "/relay/send", relay("B"), 403},
		apiCase{"agent_a relays from A, not bound in the chat", "agent_a", "POST", "/relay/send", relay("A"), 403},
		apiCase{"agent_a relays from no project", "agent_a", "POST", "/relay/send", relay(""), 403},
		apiCase{"agent_b relays from A", "agent_b", "POST", "/relay/send", relay("A"), 403},
		apiCase{"stranger relays from B", "stranger", "POST", "/relay/send", relay("B"), 403},
	)
}

func signoffBody(sessionKey string) string {
	return fmt.Sprintf(`{"session_key":%q,"request_id":"r1","nonce":"n1","title":"t","body":[{"style":"heading","text":"x"}],"confirm_label":"ok","cancel_label":"no"}`, sessionKey)
}

// startTimedOutJobs starts cron exec jobs in A that outlive their one-minute
// timeout, one with a background child and one that ignores TERM, and
// returns when their processes run.
func startTimedOutJobs(t *testing.T, sup string) time.Time {
	t.Helper()
	if procs := userProcesses("agent_a"); procs != "" {
		t.Fatalf("agent_a has processes before the jobs start:\n%s", procs)
	}
	for _, command := range []string{"sleep 600 & sleep 600", "trap '' TERM; sleep 600"} {
		id := addCronJob(t, sup, "A", command, "--timeout-mins", "1")
		if out, err := cli(sup, "cron", "exec", id); err != nil {
			t.Fatalf("cron exec %s: %v: %s", id, err, out)
		}
	}
	started := time.Now()
	if !waitFor(15*time.Second, func() bool { return sleepers() >= 3 }) {
		t.Fatalf("the jobs' sleeps did not start; agent_a runs:\n%s", userProcesses("agent_a"))
	}
	return started
}

// checkTimedOut checks that the jobs started at started still run shortly
// before their timeout and that nothing of agent_a is left soon after it.
func checkTimedOut(t *testing.T, started time.Time) {
	time.Sleep(time.Until(started.Add(55 * time.Second)))
	if n := sleepers(); n < 3 {
		t.Fatalf("only %d of the jobs' sleeps run before the timeout", n)
	}
	if !waitFor(time.Until(started.Add(90*time.Second)), func() bool { return userProcesses("agent_a") == "" }) {
		t.Fatalf("agent_a still runs after the timeout:\n%s", userProcesses("agent_a"))
	}
}

// sleepers counts agent_a's sleep processes.
func sleepers() int {
	out, _ := exec.Command("pgrep", "-c", "-x", "-u", "agent_a", "sleep").Output()
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}
