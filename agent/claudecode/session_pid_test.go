package claudecode

import (
	"os"
	"os/exec"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPidReportsTheAgentProcess(t *testing.T) {
	var running core.AgentSession = &claudeSession{cmd: &exec.Cmd{Process: &os.Process{Pid: 4242}}}
	reporter, ok := running.(core.AgentProcessReporter)
	if !ok {
		t.Fatal("claudeSession does not report its process")
	}
	if got := reporter.Pid(); got != 4242 {
		t.Errorf("Pid() = %d, want 4242", got)
	}
	if got := (&claudeSession{cmd: &exec.Cmd{}}).Pid(); got != 0 {
		t.Errorf("Pid() before start = %d, want 0", got)
	}
}
