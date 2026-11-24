package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"
)

// signoffExecLimit is how long signoff_exec may run before its outcome is
// failed and its process group is stopped.
const signoffExecLimit = 5 * time.Minute

// signoffExecInput is the JSON object signoff_exec reads on stdin.
type signoffExecInput struct {
	RequestID string         `json:"request_id"`
	Nonce     string         `json:"nonce"`
	ClickedBy SignoffClicker `json:"clicked_by"`
}

// signoffExecOutput is the JSON object signoff_exec prints on stdout.
type signoffExecOutput struct {
	Outcome string `json:"outcome"`
	Message string `json:"message"`
}

// runSignoffExec runs signoff_exec for an approved card in sessionKey's
// session and returns its outcome and one-line message. A command that is
// unset, exits non-zero, is killed at the limit or prints anything but one
// valid JSON object gives failed with no message. A command that exited zero
// is read from its stdout even when a descendant held a pipe past the limit.
//
// The command runs through the project's shell and flag without
// shell_profile, from /. Under run_as_user it runs as that user, and at the
// limit its process group gets TERM and then KILL. Otherwise it runs as
// cc-connect's own user, so nothing in a directory or profile the agent's
// user can write is run, and once it has ended the rest of its process group
// is killed.
func (e *Engine) runSignoffExec(sessionKey string, in signoffExecInput) (outcome, message string) {
	e.signoff.mu.Lock()
	command, limit := e.signoff.exec, e.signoff.execLimit
	e.signoff.mu.Unlock()
	if command == "" {
		slog.Warn("signoff: signoff_exec is not set", "request_id", in.RequestID)
		return SignoffFailed, ""
	}
	if limit <= 0 {
		limit = signoffExecLimit
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		slog.Error("signoff: encode signoff_exec input failed", "request_id", in.RequestID, "error", err)
		return SignoffFailed, ""
	}

	ctx, cancel := context.WithTimeout(e.ctx, limit)
	defer cancel()
	opts := e.commandSpawnOptions()
	runAs := opts.IsolationMode()
	cmd, err := buildProjectCommand(ctx, opts, projectCommand{
		shell: e.shell, flag: e.shellFlag, command: command, dir: "/", env: e.commandEnv(sessionKey),
	})
	if err != nil {
		slog.Error("signoff: signoff_exec not run", "request_id", in.RequestID, "error", err)
		return SignoffFailed, ""
	}
	cmd.Stdin = bytes.NewReader(stdin)
	if !runAs {
		// A descendant that left the process group and still holds a pipe
		// must not keep the session held.
		cmd.WaitDelay = 2 * time.Second
		killProcessGroupOnCancel(cmd)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	defer func() {
		errOut := stderr.Bytes()
		if runAs {
			errOut = projectCommandOutput(errOut)
		}
		s := strings.TrimSpace(string(errOut))
		if s == "" {
			return
		}
		level := slog.LevelInfo
		if outcome == SignoffFailed {
			level = slog.LevelWarn
		}
		slog.Log(context.Background(), level, "signoff: signoff_exec stderr", "request_id", in.RequestID, "stderr", truncateStr(s, 2000))
	}()

	err = cmd.Run()
	if !runAs {
		killProcessGroup(cmd)
	}
	// Run also fails when the shell exited zero but the limit or WaitDelay
	// ended while a descendant held a pipe; the shell's own exit decides.
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		if ctx.Err() != nil {
			slog.Error("signoff: signoff_exec ran past its limit or cc-connect stopped", "request_id", in.RequestID, "limit", limit)
		} else {
			slog.Error("signoff: signoff_exec failed", "request_id", in.RequestID, "error", err)
		}
		return SignoffFailed, ""
	}
	out := stdout.Bytes()
	if runAs {
		out = projectCommandOutput(out)
	}
	answer, err := parseSignoffExecOutput(out)
	if err != nil {
		slog.Error("signoff: signoff_exec printed an invalid answer", "request_id", in.RequestID, "error", err, "stdout", truncateStr(string(out), 2000))
		return SignoffFailed, ""
	}
	return answer.Outcome, answer.Message
}

// parseSignoffExecOutput reads exactly one JSON object with known fields, an
// outcome of published or refused, and a message of at most one line.
func parseSignoffExecOutput(stdout []byte) (signoffExecOutput, error) {
	var out signoffExecOutput
	dec := json.NewDecoder(bytes.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return out, errors.New("more than one JSON value")
	}
	if out.Outcome != SignoffPublished && out.Outcome != SignoffRefused {
		return out, errors.New("outcome must be published or refused")
	}
	if strings.ContainsAny(out.Message, "\r\n\u2028\u2029") {
		return out, errors.New("message must be one line")
	}
	return out, nil
}
