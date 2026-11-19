package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// signoffExecLimit is how long signoff_exec may run before its outcome is
// failed and its process group is killed.
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

// runSignoffExec runs signoff_exec for an approved card and returns its
// outcome and one-line message. A command that is unset, exits non-zero,
// prints anything but one valid JSON object or outlives the limit gives
// failed with no message. Once the command has ended, the rest of its process
// group is killed.
//
// The command runs through the project's shell and flag without
// shell_profile, from /, so nothing in a directory or profile the agent's
// user can write is run with cc-connect's rights.
func (e *Engine) runSignoffExec(in signoffExecInput) (outcome, message string) {
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
	cmd := shellExecCommand(ctx, e.shell, e.shellFlag, "", command)
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(stdin)
	// A descendant that left the process group and still holds a pipe must
	// not keep the session held.
	cmd.WaitDelay = 2 * time.Second
	killProcessGroupOnCancel(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	killProcessGroup(cmd)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		slog.Warn("signoff: signoff_exec stderr", "request_id", in.RequestID, "stderr", truncateStr(s, 2000))
	}
	if ctx.Err() != nil {
		slog.Error("signoff: signoff_exec ran past its limit or cc-connect stopped", "request_id", in.RequestID, "limit", limit)
		return SignoffFailed, ""
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		slog.Error("signoff: signoff_exec failed", "request_id", in.RequestID, "error", err)
		return SignoffFailed, ""
	}
	out, err := parseSignoffExecOutput(stdout.Bytes())
	if err != nil {
		slog.Error("signoff: signoff_exec printed an invalid answer", "request_id", in.RequestID, "error", err, "stdout", truncateStr(stdout.String(), 2000))
		return SignoffFailed, ""
	}
	return out.Outcome, out.Message
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
	if hasLineBreak(out.Message) {
		return out, errors.New("message must be one line")
	}
	return out, nil
}
