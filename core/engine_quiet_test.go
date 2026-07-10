package core

// Tests for issue #1302 — quiet mode "final message only" behaviour.
//
// Quiet mode's inline doc promised "final message only" but the
// implementation concatenated the pre-tool "lead-in" (e.g. "Let me check
// that for you...") directly with the post-tool answer, with no
// separator. The fix slices the accumulated text at the last tool_use
// boundary when mode == "quiet" and DisplayCfg.PrependPreToolText is
// false (the default). The pre-tool text and the "\n\n" separator
// between pre and post are dropped from the final reply.
//
// These tests run the same end-to-end event loop the production
// code uses (processInteractiveEvents), so they exercise all
// platform-agnostic finalization paths in one go: stream preview,
// sendChunksWithStatusFooter, the !isSilent and isSilent branches, the
// silent-hold live-frame path, and the accumulated-textParts slice point
// in EventResult.

import (
	"strings"
	"testing"
	"time"
)

// runQuietTurn drives one full turn with the given display config and
// the given event sequence. It returns whatever the platform captured
// via Reply / Send. The test framework is the same
// processInteractiveEvents + controllableAgentSession path used by the
// rest of the engine tests.
func runQuietTurn(t *testing.T, cfg DisplayCfg, events []Event) []string {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(cfg)
	e.SetReplyFooterEnabled(false) // keep final text predictable across tests

	sessionKey := "test:quiet-u1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet")
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx-quiet",
	}
	e.interactiveStates[sessionKey] = state

	for _, ev := range events {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet", time.Now(), nil, nil, state.replyCtx)
	return p.getSent()
}

// TestQuiet_Default_DropsPreToolLeadIn is the primary regression test
// for #1302. With mode = "quiet" and PrependPreToolText = false (the
// default), the user only sees the text emitted after the LAST
// tool_use. The pre-tool "Let me check that for you..." lead-in and
// the "\n\n" separator that used to glue it to the answer are both
// dropped.
func TestQuiet_Default_DropsPreToolLeadIn(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "quiet",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: false,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "Let me check that for you."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Here is the answer: /home/user."},
		{Type: EventResult, Content: "Here is the answer: /home/user.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("sent = nil, want at least one final reply")
	}
	// All sent messages should be the same final text (one card).
	// We just check the final text — pre-tool content must not appear.
	final := sent[len(sent)-1]
	if strings.Contains(final, "Let me check that for you") {
		t.Errorf("pre-tool lead-in leaked into final reply: %q", final)
	}
	if !strings.Contains(final, "Here is the answer: /home/user.") {
		t.Errorf("post-tool answer missing from final reply: %q", final)
	}
	if strings.Contains(final, "\n\n") {
		// The legacy bug glued the pre-tool text and post-tool text with
		// "\n\n". With the fix in place the pre-tool slice (including
		// the separator) is dropped, so the only "\n\n" we could see
		// would be inside the post-tool text itself.
		t.Errorf("unexpected \"\\n\\n\" in final reply (separator should be dropped): %q", final)
	}
}

// TestQuiet_Default_NoTool_KeepsAllText covers the no-tool case: there
// is no tool_use to mark a boundary, so the entire text is the
// "final message" and must be delivered verbatim. This guards against
// the fix accidentally regressing the common "user asks a question,
// agent answers directly" path.
func TestQuiet_Default_NoTool_KeepsAllText(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "quiet",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: false,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "Just a direct answer."},
		{Type: EventResult, Content: "Just a direct answer.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("sent = nil, want at least one final reply")
	}
	final := sent[len(sent)-1]
	if !strings.Contains(final, "Just a direct answer.") {
		t.Errorf("direct answer missing from final reply: %q", final)
	}
}

// TestQuiet_PrependOptIn_KeepsPreToolLeadIn verifies the opt-in path:
// with PrependPreToolText = true, the user gets the legacy
// "pre-tool + post-tool" concatenation. This is what existing
// quiet-mode users who relied on the old behaviour should set in
// their config to keep the lead-in.
//
// Note: the legacy code only inserted a "\n\n" separator between the
// pre- and post-tool slices when the platform's stream preview was
// active (`sp.canPreview()` returns true). The stub platform in this
// test does not implement stream preview, so we don't assert on the
// separator here — only that both halves of the text are present in
// the final reply, which is the user-visible behaviour the opt-in
// knob controls.
func TestQuiet_PrependOptIn_KeepsPreToolLeadIn(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "quiet",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: true,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "Let me check that for you."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Here is the answer: /home/user."},
		{Type: EventResult, Content: "Here is the answer: /home/user.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("sent = nil, want at least one final reply")
	}
	final := sent[len(sent)-1]
	if !strings.Contains(final, "Let me check that for you") {
		t.Errorf("pre-tool lead-in missing with PrependPreToolText=true: %q", final)
	}
	if !strings.Contains(final, "Here is the answer: /home/user.") {
		t.Errorf("post-tool answer missing with PrependPreToolText=true: %q", final)
	}
}

// TestQuiet_Default_OnlyLastToolSurfaces verifies that when multiple
// tool_uses happen, the user still only sees the text after the LAST
// one. An intermediate text block followed by another tool_use should
// also be treated as pre-tool lead-in (and dropped), because the
// final reply is anchored on the most recent tool_use.
func TestQuiet_Default_OnlyLastToolSurfaces(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "quiet",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: false,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "First lead-in (drop me)."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Intermediate result (drop me too)."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Final answer: /home/user."},
		{Type: EventResult, Content: "Final answer: /home/user.", Done: true},
	})
	if len(sent) == 0 {
		t.Fatal("sent = nil, want at least one final reply")
	}
	final := sent[len(sent)-1]
	if strings.Contains(final, "First lead-in") {
		t.Errorf("first pre-tool lead-in leaked: %q", final)
	}
	if strings.Contains(final, "Intermediate result") {
		t.Errorf("intermediate text (between two tool_uses) leaked: %q", final)
	}
	if !strings.Contains(final, "Final answer: /home/user.") {
		t.Errorf("final answer missing: %q", final)
	}
}

// TestQuiet_StreamingCard_LiveFramesDropPreToolLeadIn verifies the live
// card frames (streamCard.Update), not just the finalized card: in quiet
// mode the intermediate update after a second tool_use must show only the
// current post-tool segment, never the earlier pre-tool lead-ins. Without
// the fix the card accumulated every lead-in as it streamed and only
// collapsed at finalize.
func TestQuiet_StreamingCard_LiveFramesDropPreToolLeadIn(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "teams"},
		card:               card,
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: false})
	e.SetReplyFooterEnabled(false)

	sessionKey := "teams:quiet-live"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet-live")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-live"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range []Event{
		{Type: EventText, Content: "First lead-in."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "Second lead-in."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Final answer."},
		{Type: EventResult, Content: "Final answer.", Done: true},
	} {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-live", time.Now(), nil, nil, state.replyCtx)

	updates := card.updateBodies()
	if len(updates) == 0 {
		t.Fatal("expected at least one live card update")
	}
	// The last live frame (rendered for "Final answer.") must contain only
	// the post-last-tool segment — no earlier lead-ins.
	last := updates[len(updates)-1]
	if !strings.Contains(last, "Final answer.") {
		t.Errorf("last live frame missing the post-tool answer: %q", last)
	}
	if strings.Contains(last, "First lead-in") || strings.Contains(last, "Second lead-in") {
		t.Errorf("live card frame accumulated a pre-tool lead-in: %q", last)
	}
	// And the finalized card stays clean too (mirrors the live frame).
	if strings.Contains(card.finalContent(), "lead-in") {
		t.Errorf("finalized card leaked a lead-in: %q", card.finalContent())
	}
}

// TestQuiet_StreamingCard_SilentAfterToolNoMarkerFlash guards the silent-hold
// window in quiet mode: when the agent narrates a lead-in, runs a tool, then
// resolves to a bare NO_REPLY marker after the last tool_use, neither the live
// card frames nor the finalized card may render the raw NO_REPLY marker.
// The marker window that decides silentHold must track the post-tool slice
// (what quiet mode actually delivers), not the segmentStart window — otherwise
// the pre-tool lead-in defeats silentHold and the marker flashes into the card.
func TestQuiet_StreamingCard_SilentAfterToolNoMarkerFlash(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "teams"},
		card:               card,
	}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: false})
	e.SetReplyFooterEnabled(false)

	sessionKey := "teams:quiet-silent"
	session := e.sessions.GetOrCreateActive(sessionKey)
	sess := newControllableSession("s-quiet-silent")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx-quiet-silent"}
	e.interactiveStates[sessionKey] = state

	for _, ev := range []Event{
		{Type: EventText, Content: "Working on it."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"},
		{Type: EventText, Content: "NO_REPLY"},
		{Type: EventResult, Content: "NO_REPLY", Done: true},
	} {
		sess.events <- ev
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-quiet-silent", time.Now(), nil, nil, state.replyCtx)

	// No live frame may render the raw NO_REPLY marker.
	for i, body := range card.updateBodies() {
		if strings.Contains(body, "NO_REPLY") {
			t.Errorf("live card frame %d flashed the NO_REPLY marker: %q", i, body)
		}
	}
	// The finalized card must not render the marker either.
	if strings.Contains(card.finalContent(), "NO_REPLY") {
		t.Errorf("finalized card leaked the NO_REPLY marker: %q", card.finalContent())
	}
	// A silent reply drops the pre-tool lead-in too: quiet mode delivers only
	// the post-tool slice, so the finalized card must not surface "Working on
	// it." — text the user never saw stream and that quiet mode drops elsewhere.
	if strings.Contains(card.finalContent(), "Working on it") {
		t.Errorf("finalized silent card leaked the pre-tool lead-in: %q", card.finalContent())
	}
}

// TestQuiet_QueuedTurnAfterTool_NoPanic is a regression test for a
// slice-bounds panic: postLastToolStart is set at a tool boundary but was not
// reset in the queued-turn reset block. In quiet mode the first EventText of a
// queued follow-up turn reads textParts[postLastToolStart:] before appending —
// with textParts freshly nil and postLastToolStart stale from the prior turn,
// that panics ("slice bounds out of range"), crashing the event-loop goroutine
// (no recover in engine.go) and taking down every active session.
func TestQuiet_QueuedTurnAfterTool_NoPanic(t *testing.T) {
	card := &recordingStreamCard{}
	p := &recordingStreamCardPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "teams"},
		card:               card,
	}
	sess := newQueuingSession("qs-quiet-panic")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ThinkingMessages: false, ToolMessages: false, PrependPreToolText: false})
	e.SetReplyFooterEnabled(false)

	key := "teams:quiet-queued"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{
		agentSession: sess,
		platform:     p,
		replyCtx:     "ctx-turn1",
		pendingMessages: []queuedMessage{
			{platform: p, replyCtx: "ctx-turn2", content: "queued-msg"},
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	go func() {
		// Turn 1: text then a tool_use (sets postLastToolStart) then result.
		sess.events <- Event{Type: EventText, Content: "Checking..."}
		sess.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "ls"}
		sess.events <- Event{Type: EventResult, Content: "done", Done: true}
		// Wait for the queued message's Send() before pushing turn 2 events.
		sess.sendMu.Lock()
		for len(sess.sendCalls) == 0 {
			sess.sendMu.Unlock()
			time.Sleep(5 * time.Millisecond)
			sess.sendMu.Lock()
		}
		sess.sendMu.Unlock()
		// Turn 2: first EventText is where the stale postLastToolStart detonates.
		sess.events <- Event{Type: EventText, Content: "Hello again"}
		sess.events <- Event{Type: EventResult, Content: "Hello again", Done: true}
	}()

	session.AddHistory("user", "initial-msg")
	sendDone := make(chan error, 1)
	sendDone <- nil

	done := make(chan struct{})
	panicked := make(chan any, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicked <- r
			}
			close(done)
		}()
		e.processInteractiveEvents(state, session, e.sessions, key, "msg1", time.Now(), nil, sendDone, nil)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processInteractiveEvents did not complete in time")
	}
	select {
	case r := <-panicked:
		t.Fatalf("queued quiet turn after a tool panicked: %v", r)
	default:
	}
}

// TestCompact_NotAffectedByQuietFix guards the other display modes
// against the new quiet-mode slicing. The bug only exists in quiet
// mode; compact and full mode must continue to deliver the full
// accumulated textParts in their existing forms.
func TestCompact_NotAffectedByQuietFix(t *testing.T) {
	cfg := DisplayCfg{
		Mode:               "compact",
		ThinkingMessages:   false,
		ToolMessages:       false,
		PrependPreToolText: false,
	}
	sent := runQuietTurn(t, cfg, []Event{
		{Type: EventText, Content: "Pre-tool lead-in."},
		{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"},
		{Type: EventText, Content: "Post-tool answer."},
		{Type: EventResult, Content: "Post-tool answer.", Done: true},
	})
	// Compact mode flushes each text segment as a separate card. We
	// don't assert exact message count (it's governed by compact-mode
	// segment flushing rules) — only that the pre-tool lead-in
	// appears in some sent message. The point is: the quiet-mode
	// slicing must NOT bleed into compact mode.
	found := false
	for _, s := range sent {
		if strings.Contains(s, "Pre-tool lead-in.") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("compact mode unexpectedly dropped pre-tool text: sent = %v", sent)
	}
}
