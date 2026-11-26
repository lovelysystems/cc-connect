package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type transcriptHarness struct {
	t       *testing.T
	e       *Engine
	p       *stubPlatformEngine
	session *Session
	agent   *controllableAgentSession
	state   *interactiveState
	key     string
	buf     *bytes.Buffer
}

func newTranscriptHarness(t *testing.T, display DisplayCfg) *transcriptHarness {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("proj", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(display)
	buf := &bytes.Buffer{}
	e.SetTranscriptLogger(slog.New(slog.NewJSONHandler(buf, nil)))

	key := "test:chat:u1"
	agentSession := newControllableSession("agent-1")
	agentSession.events = make(chan Event, 32)
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state
	return &transcriptHarness{
		t: t, e: e, p: p,
		session: e.sessions.GetOrCreateActive(key),
		agent:   agentSession,
		state:   state,
		key:     key,
		buf:     buf,
	}
}

func (h *transcriptHarness) run(events ...Event) {
	h.t.Helper()
	for _, ev := range events {
		h.agent.events <- ev
	}
	h.e.processInteractiveEvents(h.state, h.session, h.e.sessions, h.key, "m1", time.Now(), nil, nil, h.state.replyCtx)
}

func (h *transcriptHarness) records(kind string) []map[string]any {
	h.t.Helper()
	return transcriptRecords(h.t, h.buf, kind)
}

func transcriptRecords(t *testing.T, buf *bytes.Buffer, kind string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("transcript line is not JSON: %q", sc.Text())
		}
		if rec["msg"] != "transcript" {
			t.Fatalf("record msg = %v, want transcript", rec["msg"])
		}
		if rec["kind"] == kind {
			out = append(out, rec)
		}
	}
	return out
}

func onlyRecord(t *testing.T, recs []map[string]any) map[string]any {
	t.Helper()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1: %v", len(recs), recs)
	}
	return recs[0]
}

func TestTranscript_UserRecordHoldsInboundMessageVerbatim(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("proj", &stubAgent{}, []Platform{p}, "", LangEnglish)
	buf := &bytes.Buffer{}
	e.SetTranscriptLogger(slog.New(slog.NewJSONHandler(buf, nil)))

	content := "/status\nsecond line"
	e.handleMessage(p, &Message{SessionKey: "test:chat:u1", Platform: "test", UserID: "u1", Content: content, ReplyCtx: "ctx"})

	rec := onlyRecord(t, transcriptRecords(t, buf, "user"))
	if rec["content"] != content {
		t.Errorf("content = %q, want %q", rec["content"], content)
	}
	if rec["project"] != "proj" || rec["session_key"] != "test:chat:u1" {
		t.Errorf("project/session_key = %v/%v", rec["project"], rec["session_key"])
	}
}

func TestTranscript_SwitchOffLogsNoChatTextAtDebug(t *testing.T) {
	prev := slog.Default()
	logBuf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	agent := &stubReplyFooterAgent{stubModelModeAgent: stubModelModeAgent{model: "m-1"}, workDir: t.TempDir()}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("proj", agent, []Platform{p}, "", LangEnglish)
	e.SetReplyFooterEnabled(true)
	e.SetDisplayConfig(DisplayCfg{ToolMaxLen: 500})

	e.handleMessage(p, &Message{SessionKey: "test:chat:u1", Platform: "test", UserID: "u1", Content: "/status USERMARKER", ReplyCtx: "ctx"})

	key := "test:chat:u1"
	agentSession := newControllableSession("agent-1")
	agentSession.events = make(chan Event, 32)
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx", agent: agent}
	e.interactiveStates[key] = state
	reply := "REPLYMARKER " + strings.Repeat("x", 2000) + " REPLYEND"
	ok := true
	for _, id := range []string{"a", "b", "c"} {
		agentSession.events <- Event{Type: EventToolUse, ToolCallID: id, ToolName: "Bash", ToolInput: "echo " + id}
		agentSession.events <- Event{Type: EventToolResult, ToolCallID: id, ToolResult: "out", ToolSuccess: &ok}
	}
	agentSession.events <- Event{Type: EventText, Content: reply}
	agentSession.events <- Event{Type: EventResult, Content: reply, Done: true}
	e.processInteractiveEvents(state, e.sessions.GetOrCreateActive(key), e.sessions, key, "m1", time.Now(), nil, nil, "ctx")

	logs := logBuf.String()
	for _, marker := range []string{"USERMARKER", "REPLYMARKER", "REPLYEND", "msg=transcript"} {
		if strings.Contains(logs, marker) {
			t.Errorf("log contains %q with the switch off", marker)
		}
	}
	if sent := p.getSent(); len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "REPLYEND") {
		t.Fatalf("reply was not delivered: %v", sent)
	}
}

func TestTranscript_ParallelToolCallsPairByID(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	ok := true
	h.run(
		Event{Type: EventToolUse, ToolCallID: "a", ToolName: "Bash", ToolInput: "sleep 3"},
		Event{Type: EventToolUse, ToolCallID: "b", ToolName: "Bash", ToolInput: "sleep 1"},
		Event{Type: EventToolResult, ToolCallID: "b", ToolSuccess: &ok},
		Event{Type: EventToolResult, ToolCallID: "a", ToolSuccess: &ok},
		Event{Type: EventResult, Content: "done", Done: true},
	)
	recs := h.records("tool")
	if len(recs) != 2 {
		t.Fatalf("got %d tool records, want 2: %v", len(recs), recs)
	}
	if recs[0]["input"] != "sleep 1" || recs[1]["input"] != "sleep 3" {
		t.Errorf("inputs = %v, %v; want sleep 1 then sleep 3", recs[0]["input"], recs[1]["input"])
	}
	for _, rec := range recs {
		if rec["tool"] != "Bash" || rec["outcome"] != "ok" {
			t.Errorf("record = %v, want Bash ok", rec)
		}
		if _, has := rec["duration_ms"]; !has {
			t.Errorf("record has no duration: %v", rec)
		}
		if rec["session"] != h.session.ID || rec["session_key"] != h.key || rec["project"] != "proj" {
			t.Errorf("record identity = %v", rec)
		}
	}
}

func TestTranscript_ErroredCallCarriesErrorText(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	failed := false
	denial := "Permission to use Bash with command rm -rf / has been denied."
	h.run(
		Event{Type: EventToolUse, ToolCallID: "a", ToolName: "Bash", ToolInput: "rm -rf /"},
		Event{Type: EventToolResult, ToolCallID: "a", ToolResult: denial, ToolSuccess: &failed},
		Event{Type: EventResult, Content: "done", Done: true},
	)
	rec := onlyRecord(t, h.records("tool"))
	if rec["outcome"] != "error" || rec["input"] != "rm -rf /" || rec["error"] != denial {
		t.Errorf("record = %v", rec)
	}
	if _, has := rec["exit_code"]; has {
		t.Errorf("exit_code set without one reported: %v", rec)
	}
}

func TestTranscript_ExitCodeRecordedWhenReported(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	failed := false
	code := 128
	h.run(
		Event{Type: EventToolUse, ToolCallID: "a", ToolName: "Bash", ToolInput: "git push"},
		Event{Type: EventToolResult, ToolCallID: "a", ToolResult: "Exit code 128\nfatal", ToolSuccess: &failed, ToolExitCode: &code},
		Event{Type: EventResult, Content: "done", Done: true},
	)
	rec := onlyRecord(t, h.records("tool"))
	if rec["exit_code"] != float64(128) {
		t.Errorf("exit_code = %v, want 128", rec["exit_code"])
	}
}

func TestTranscript_QuietReplyMatchesDeliveredMessage(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{Mode: "quiet", PrependPreToolText: true})
	h.run(
		Event{Type: EventText, Content: "Let me look."},
		Event{Type: EventToolUse, ToolCallID: "a", ToolName: "Read", ToolInput: "/a"},
		Event{Type: EventText, Content: "Found it."},
		Event{Type: EventResult, Content: "Found it.", Done: true},
	)
	rec := onlyRecord(t, h.records("reply"))
	sent := h.p.getSent()
	if len(sent) == 0 {
		t.Fatal("nothing delivered")
	}
	if rec["content"] != sent[len(sent)-1] {
		t.Errorf("reply record = %q, delivered = %q", rec["content"], sent[len(sent)-1])
	}
	if !strings.Contains(rec["content"].(string), "Let me look.") {
		t.Errorf("reply record lost the pre-tool text: %q", rec["content"])
	}
}

func TestTranscript_SilentTurnHasNoReplyRecord(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	h.run(Event{Type: EventResult, Content: "NO_REPLY", Done: true})
	if recs := h.records("reply"); len(recs) != 0 {
		t.Errorf("silent turn wrote reply records: %v", recs)
	}
}

// exitedAgentSession is an agent whose process already exited: its event
// channel is closed, so Close must not close it again.
type exitedAgentSession struct{ *controllableAgentSession }

func (exitedAgentSession) Close() error { return nil }

func TestTranscript_AbnormalCloseRecordsDeliveredReply(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	h.state.agentSession = exitedAgentSession{h.agent}
	h.agent.events <- Event{Type: EventText, Content: "partial answer"}
	close(h.agent.events)
	h.e.processInteractiveEvents(h.state, h.session, h.e.sessions, h.key, "m1", time.Now(), nil, nil, h.state.replyCtx)
	rec := onlyRecord(t, h.records("reply"))
	if rec["content"] != "partial answer" {
		t.Errorf("reply = %q, want partial answer", rec["content"])
	}
}

func TestTranscript_UnpairedCalls(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	ok := true
	h.run(
		Event{Type: EventToolResult, ToolCallID: "orphan", ToolName: "AskUserQuestion", ToolSuccess: &ok},
		Event{Type: EventToolUse, ToolCallID: "never", ToolName: "Bash", ToolInput: "make"},
		Event{Type: EventResult, Content: "done", Done: true},
	)
	recs := h.records("tool")
	if len(recs) != 2 {
		t.Fatalf("got %d tool records, want 2: %v", len(recs), recs)
	}
	if _, has := recs[0]["duration_ms"]; has || recs[0]["tool"] != "AskUserQuestion" {
		t.Errorf("unmatched result record = %v, want no duration", recs[0])
	}
	if recs[1]["outcome"] != "no_result" || recs[1]["input"] != "make" {
		t.Errorf("pending call record = %v, want no_result", recs[1])
	}
}

func TestTranscript_CallsWithoutIDs(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	h.run(
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "one"},
		Event{Type: EventToolResult, ToolName: "Bash"},
		Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "two"},
		Event{Type: EventToolResult, ToolName: "Bash"},
		Event{Type: EventToolUse, ToolName: "Read", ToolInput: "/x"},
		Event{Type: EventToolUse, ToolName: "Read", ToolInput: "/y"},
		Event{Type: EventToolResult, ToolName: "Read"},
		Event{Type: EventResult, Content: "done", Done: true},
	)
	recs := h.records("tool")
	if len(recs) != 5 {
		t.Fatalf("got %d tool records, want 5: %v", len(recs), recs)
	}
	if recs[0]["input"] != "one" || recs[1]["input"] != "two" {
		t.Errorf("sequential calls paired out of order: %v, %v", recs[0], recs[1])
	}
	for _, rec := range recs[:2] {
		if rec["outcome"] != "unknown" {
			t.Errorf("outcome = %v, want unknown when the agent reports none", rec["outcome"])
		}
	}
	if _, has := recs[2]["duration_ms"]; has {
		t.Errorf("ambiguous result was paired: %v", recs[2])
	}
}

func TestTranscript_InputRedactedBeforeCap(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	input := strings.Repeat("x", 290) + " API_TOKEN=supersecretvalue1234567890"
	h.run(
		Event{Type: EventToolUse, ToolCallID: "a", ToolName: "Bash", ToolInput: input},
		Event{Type: EventToolResult, ToolCallID: "a"},
		Event{Type: EventResult, Content: "done", Done: true},
	)
	got := onlyRecord(t, h.records("tool"))["input"].(string)
	if strings.Contains(got, "supersecret") {
		t.Errorf("secret leaked through the cap: %q", got)
	}
	if n := len([]rune(got)); n > 301 {
		t.Errorf("input has %d runes, want at most 300 plus an ellipsis", n)
	}
}

func TestTranscript_MultiWorkspaceKeepsPlatformKey(t *testing.T) {
	h := newTranscriptHarness(t, DisplayCfg{})
	h.state.workspaceDir = "/ws/one"
	h.key = "/ws/one:test:chat:u1"
	h.run(
		Event{Type: EventToolUse, ToolCallID: "a", ToolName: "Bash", ToolInput: "pwd"},
		Event{Type: EventToolResult, ToolCallID: "a"},
		Event{Type: EventResult, Content: "done", Done: true},
	)
	for _, kind := range []string{"tool", "reply"} {
		rec := onlyRecord(t, h.records(kind))
		if rec["session_key"] != "test:chat:u1" || rec["workspace"] != "/ws/one" {
			t.Errorf("%s record key/workspace = %v/%v", kind, rec["session_key"], rec["workspace"])
		}
	}
}
