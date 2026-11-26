package core

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

// transcriptTextMax caps tool input and error excerpts in transcript records.
const transcriptTextMax = 300

func (e *Engine) transcriptUser(msg *Message) {
	if e.transcript == nil {
		return
	}
	e.transcript.LogAttrs(context.Background(), slog.LevelInfo, "transcript",
		slog.String("kind", "user"),
		slog.String("project", e.name),
		slog.String("session_key", msg.SessionKey),
		slog.String("content", msg.Content),
	)
}

// transcriptTurn writes the reply and tool records of one turn loop. With a nil
// logger it writes nothing.
type transcriptTurn struct {
	logger     *slog.Logger
	project    string
	sessionKey string
	workspace  string
	session    *Session
	pending    []pendingToolCall
}

type pendingToolCall struct {
	id    string
	name  string
	input string
	start time.Time
}

// newTranscriptTurn takes the turn loop's key, which in multi-workspace mode is
// "<workspace>:<platform key>"; records carry the platform key so one filter
// finds the user record and the turn's records together.
func (e *Engine) newTranscriptTurn(state *interactiveState, session *Session, sessionKey string) *transcriptTurn {
	t := &transcriptTurn{logger: e.transcript, project: e.name, sessionKey: sessionKey, session: session}
	if ws := state.workspaceDir; ws != "" && strings.HasPrefix(sessionKey, ws+":") {
		t.workspace = ws
		t.sessionKey = strings.TrimPrefix(sessionKey, ws+":")
	}
	return t
}

func (t *transcriptTurn) write(kind string, attrs ...slog.Attr) {
	base := []slog.Attr{
		slog.String("kind", kind),
		slog.String("project", t.project),
		slog.String("session_key", t.sessionKey),
		slog.String("session", t.session.ID),
	}
	if id := t.session.GetAgentSessionID(); id != "" {
		base = append(base, slog.String("agent_session", id))
	}
	if t.workspace != "" {
		base = append(base, slog.String("workspace", t.workspace))
	}
	t.logger.LogAttrs(context.Background(), slog.LevelInfo, "transcript", append(base, attrs...)...)
}

func (t *transcriptTurn) reply(content string) {
	if t.logger == nil {
		return
	}
	t.write("reply", slog.String("content", content))
}

func (t *transcriptTurn) toolUse(ev Event) {
	if t.logger == nil {
		return
	}
	t.pending = append(t.pending, pendingToolCall{
		id:    ev.ToolCallID,
		name:  ev.ToolName,
		input: transcriptExcerpt(ev.ToolInput),
		start: time.Now(),
	})
}

func (t *transcriptTurn) toolResult(ev Event) {
	if t.logger == nil {
		return
	}
	name, input := ev.ToolName, transcriptExcerpt(ev.ToolInput)
	var start time.Time
	if i := t.match(ev); i >= 0 {
		call := t.pending[i]
		t.pending = append(t.pending[:i], t.pending[i+1:]...)
		name, input, start = call.name, call.input, call.start
	}

	outcome := toolOutcome(ev)
	attrs := []slog.Attr{
		slog.String("tool", name),
		slog.String("input", input),
		slog.String("outcome", outcome),
	}
	if ev.ToolExitCode != nil {
		attrs = append(attrs, slog.Int("exit_code", *ev.ToolExitCode))
	}
	if !start.IsZero() {
		attrs = append(attrs, slog.Int64("duration_ms", time.Since(start).Milliseconds()))
	}
	if outcome == "error" {
		text := ev.ToolResult
		if strings.TrimSpace(text) == "" {
			text = ev.Content
		}
		attrs = append(attrs, slog.String("error", transcriptExcerpt(strings.TrimSpace(text))))
	}
	t.write("tool", attrs...)
}

// match finds the pending call a result belongs to. Without a call ID it pairs
// only when exactly one call of that tool is pending, since parallel calls of
// one tool cannot be told apart.
func (t *transcriptTurn) match(ev Event) int {
	if ev.ToolCallID != "" {
		for i, call := range t.pending {
			if call.id == ev.ToolCallID {
				return i
			}
		}
		return -1
	}
	found := -1
	for i, call := range t.pending {
		if call.name == ev.ToolName {
			if found >= 0 {
				return -1
			}
			found = i
		}
	}
	return found
}

// flush records calls that got no result before the turn ended.
func (t *transcriptTurn) flush() {
	if t.logger == nil {
		return
	}
	for _, call := range t.pending {
		t.write("tool",
			slog.String("tool", call.name),
			slog.String("input", call.input),
			slog.String("outcome", "no_result"),
		)
	}
	t.pending = nil
}

func toolOutcome(ev Event) string {
	switch {
	case ev.ToolSuccess != nil && !*ev.ToolSuccess,
		strings.EqualFold(ev.ToolStatus, "failed"), strings.EqualFold(ev.ToolStatus, "error"):
		return "error"
	case ev.ToolSuccess != nil && *ev.ToolSuccess,
		strings.EqualFold(ev.ToolStatus, "completed"):
		return "ok"
	default:
		return "unknown"
	}
}

// transcriptExcerpt redacts before it cuts, so the cut never leaves part of a
// secret behind.
func transcriptExcerpt(text string) string {
	text = RedactToolText(text)
	if utf8.RuneCountInString(text) <= transcriptTextMax {
		return text
	}
	return string([]rune(text)[:transcriptTextMax]) + "…"
}
