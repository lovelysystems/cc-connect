package claudecode

// Pid returns the pid of the Claude Code process, or 0 before it started.
// Under run_as_user it is the sudo wrapper's pid, which the CLI runs under.
func (cs *claudeSession) Pid() int {
	if cs.cmd == nil || cs.cmd.Process == nil {
		return 0
	}
	return cs.cmd.Process.Pid
}
