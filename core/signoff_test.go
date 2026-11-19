package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- request and card ---

func signoffSampleBody() []SignoffLine {
	return []SignoffLine{
		{CardTextHeading, "ENTRIES"},
		{CardTextName, "winterklaenge-am-see-2027"},
		{CardTextDetail, "www.radio24.ch · www.radio32.ch"},
		{CardTextLink, "🔗 https://tickets.example.ch/winterklaenge"},
		{CardTextLink, "🔗 https://www.seebad-example.ch/anfahrt"},
		{CardTextName, "herbstfest-2026"},
		{CardTextDetail, "www.radio24.ch"},
		{CardTextName, "test-event-november-2026"},
		{CardTextDetail, "www.radio32.ch"},
		{CardTextHeading, "LISTS"},
		{CardTextName, "artikel-win"},
		{CardTextDetail, "www.radio32.ch"},
		{CardTextName, "events-uebersicht"},
		{CardTextDetail, "www.radio24.ch"},
	}
}

func signoffTestRequest(sessionKey, requestID string) SignoffRequest {
	return SignoffRequest{
		SessionKey:   sessionKey,
		RequestID:    requestID,
		Nonce:        "nonce-" + requestID,
		Title:        "5 changes",
		Body:         signoffSampleBody(),
		ConfirmLabel: "Approve",
		CancelLabel:  "Cancel",
	}
}

func TestSignoffCardFromTheSampleRequest(t *testing.T) {
	req := signoffTestRequest("test:u1", "r1")
	req.Nonce = "n1"
	if err := req.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	card := buildSignoffCard(&req)

	if card.Header == nil || card.Header.Title != "5 changes" {
		t.Fatalf("header = %+v, want title 5 changes", card.Header)
	}
	body := signoffSampleBody()
	if len(card.Elements) != len(body)+1 {
		t.Fatalf("elements = %d, want %d lines and one button row", len(card.Elements), len(body))
	}
	for i, line := range body {
		got, ok := card.Elements[i].(CardText)
		if !ok || got.Text != line.Text || got.Style != line.Style {
			t.Fatalf("element %d = %#v, want CardText %q in %q", i, card.Elements[i], line.Text, line.Style)
		}
	}
	actions, ok := card.Elements[len(body)].(CardActions)
	if !ok || len(actions.Buttons) != 2 {
		t.Fatalf("last element = %#v, want two buttons", card.Elements[len(body)])
	}
	confirm, cancel := actions.Buttons[0], actions.Buttons[1]
	if confirm.Text != "Approve" || confirm.Type != "primary" || confirm.Value != "signoff:r1:n1" {
		t.Fatalf("confirm = %+v", confirm)
	}
	if cancel.Text != "Cancel" || cancel.Type != "default" || cancel.Value != "signoff:r1:n1:cancel" {
		t.Fatalf("cancel = %+v", cancel)
	}
}

func TestSignoffCardTextKeepsMarkupAsWritten(t *testing.T) {
	texts := []string{"**x**", "<b>x</b>", `{"type":"AdaptiveCard"}`, "[a](b)", "{{DATE(2026-11-17T10:42:00Z)}}"}
	req := signoffTestRequest("test:u1", "r1")
	req.Body = nil
	for _, text := range texts {
		req.Body = append(req.Body, SignoffLine{CardTextName, text})
	}
	if err := req.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	card := buildSignoffCard(&req)
	for i, text := range texts {
		if got := card.Elements[i].(CardText).Text; got != text {
			t.Fatalf("element %d text = %q, want %q", i, got, text)
		}
	}
}

func TestSignoffRequestRefusals(t *testing.T) {
	cases := map[string]func(*SignoffRequest){
		"missing session_key":   func(r *SignoffRequest) { r.SessionKey = "" },
		"missing request_id":    func(r *SignoffRequest) { r.RequestID = "" },
		"missing nonce":         func(r *SignoffRequest) { r.Nonce = "" },
		"missing title":         func(r *SignoffRequest) { r.Title = "" },
		"missing confirm_label": func(r *SignoffRequest) { r.ConfirmLabel = "" },
		"missing cancel_label":  func(r *SignoffRequest) { r.CancelLabel = "" },
		"empty body":            func(r *SignoffRequest) { r.Body = nil },
		"unknown style":         func(r *SignoffRequest) { r.Body[0].Style = "bold" },
		"empty line text":       func(r *SignoffRequest) { r.Body[1].Text = "" },
		"colon in request_id":   func(r *SignoffRequest) { r.RequestID = "a:b" },
		"colon in nonce":        func(r *SignoffRequest) { r.Nonce = "a:b" },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			req := signoffTestRequest("test:u1", "r1")
			spoil(&req)
			if err := req.validate(); err == nil {
				t.Fatal("validate accepted the request")
			}
		})
	}
}

// --- engine harness ---

// signoffTestSession is an agent session whose turns the test answers.
type signoffTestSession struct {
	events  chan Event
	prompts chan string
	pid     int
}

func newSignoffTestSession() *signoffTestSession {
	return &signoffTestSession{events: make(chan Event, 8), prompts: make(chan string, 16), pid: 100}
}

func (s *signoffTestSession) Send(prompt string, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.prompts <- prompt
	return nil
}
func (s *signoffTestSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *signoffTestSession) Events() <-chan Event                             { return s.events }
func (s *signoffTestSession) CurrentSessionID() string                         { return "signoff-session" }
func (s *signoffTestSession) Alive() bool                                      { return true }
func (s *signoffTestSession) Close() error                                     { return nil }
func (s *signoffTestSession) Pid() int                                         { return s.pid }

func (s *signoffTestSession) nextPrompt(t *testing.T) string {
	t.Helper()
	select {
	case p := <-s.prompts:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no prompt reached the agent")
		return ""
	}
}

func (s *signoffTestSession) noPrompt(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case p := <-s.prompts:
		t.Fatalf("unexpected prompt %q", p)
	case <-time.After(wait):
	}
}

func (s *signoffTestSession) answer(text string) {
	s.events <- Event{Type: EventResult, Content: text, Done: true}
}

type signoffTestAgent struct {
	session *signoffTestSession
	runAs   string
}

func (a *signoffTestAgent) Name() string { return "stub" }
func (a *signoffTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}
func (a *signoffTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) { return nil, nil }
func (a *signoffTestAgent) Stop() error                                            { return nil }
func (a *signoffTestAgent) GetRunAsUser() string                                   { return a.runAs }

// signoffTestPlatform records, in order, what reaches the thread: messages,
// streaming cards, signoff cards and their redraws.
type signoffTestPlatform struct {
	mu       sync.Mutex
	log      []string
	handles  map[string]*signoffTestHandle
	failPost bool
}

func (p *signoffTestPlatform) record(entry string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log = append(p.log, entry)
}

func (p *signoffTestPlatform) entries() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.log...)
}

func (p *signoffTestPlatform) Name() string               { return "test" }
func (p *signoffTestPlatform) Start(MessageHandler) error { return nil }
func (p *signoffTestPlatform) Stop() error                { return nil }
func (p *signoffTestPlatform) Reply(_ context.Context, _ any, content string) error {
	p.record("msg:" + content)
	return nil
}
func (p *signoffTestPlatform) Send(_ context.Context, _ any, content string) error {
	p.record("msg:" + content)
	return nil
}

func (p *signoffTestPlatform) CreateStreamingCard(context.Context, any) (StreamingCard, error) {
	p.record("stream")
	return &signoffTestStream{p: p}, nil
}

func (p *signoffTestPlatform) PostSignoffCard(_ context.Context, replyCtx any, card *Card, onClick func(string, SignoffClicker)) (SignoffCardHandle, error) {
	p.mu.Lock()
	fail := p.failPost
	p.mu.Unlock()
	actions := card.Elements[len(card.Elements)-1].(CardActions)
	id := strings.Split(actions.Buttons[0].Value, ":")[1]
	if fail {
		return nil, errors.New("post refused")
	}
	h := &signoffTestHandle{p: p, id: id, thread: fmt.Sprint(replyCtx), card: card, onClick: onClick}
	p.mu.Lock()
	if p.handles == nil {
		p.handles = make(map[string]*signoffTestHandle)
	}
	p.handles[id] = h
	p.log = append(p.log, "card:"+id)
	p.mu.Unlock()
	return h, nil
}

type signoffTestStream struct{ p *signoffTestPlatform }

func (s *signoffTestStream) Update(context.Context, string) error { return nil }
func (s *signoffTestStream) Finalize(_ context.Context, content string) error {
	s.p.record("final:" + content)
	return nil
}
func (s *signoffTestStream) Failed() bool { return false }

type signoffTestHandle struct {
	p       *signoffTestPlatform
	id      string
	thread  string
	card    *Card
	onClick func(string, SignoffClicker)
}

func (h *signoffTestHandle) Thread() string { return h.thread }
func (h *signoffTestHandle) Redraw(_ context.Context, mark SignoffMark) error {
	kind := map[SignoffMarkKind]string{SignoffMarkApproved: "approved", SignoffMarkCancelled: "cancelled", SignoffMarkReplaced: "replaced"}[mark.Kind]
	h.p.record("redraw:" + h.id + ":" + kind + ":" + mark.By)
	return nil
}

func (h *signoffTestHandle) approve(by string) {
	actions := h.card.Elements[len(h.card.Elements)-1].(CardActions)
	h.onClick(actions.Buttons[0].Value, SignoffClicker{ID: "aad-" + by, Name: by})
}

func (h *signoffTestHandle) cancel(by string) {
	actions := h.card.Elements[len(h.card.Elements)-1].(CardActions)
	h.onClick(actions.Buttons[1].Value, SignoffClicker{ID: "aad-" + by, Name: by})
}

func (p *signoffTestPlatform) waitFor(t *testing.T, entry string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for i, got := range p.entries() {
			if got == entry {
				return i
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%q never reached the thread; log: %q", entry, p.entries())
	return -1
}

func (p *signoffTestPlatform) has(entry string) bool {
	for _, got := range p.entries() {
		if got == entry {
			return true
		}
	}
	return false
}

func (p *signoffTestPlatform) handle(t *testing.T, id string) *signoffTestHandle {
	t.Helper()
	p.waitFor(t, "card:"+id)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handles[id]
}

const signoffTestKey = "test:thread-1"

type signoffHarness struct {
	e     *Engine
	p     *signoffTestPlatform
	agent *signoffTestSession
}

func newSignoffHarness(t *testing.T) *signoffHarness {
	t.Helper()
	as := newSignoffTestSession()
	p := &signoffTestPlatform{}
	e := NewEngine("test", &signoffTestAgent{session: as, runAs: "claude"}, []Platform{p}, "", LangEnglish)
	e.SetShell("sh", "-c", "")
	t.Cleanup(func() { _ = e.Stop() })
	return &signoffHarness{e: e, p: p, agent: as}
}

func (h *signoffHarness) say(id, text string) {
	h.e.ReceiveMessage(h.p, &Message{
		SessionKey: signoffTestKey,
		Platform:   "test",
		MessageID:  id,
		UserID:     "u1",
		UserName:   "Lovely Editor",
		Content:    text,
		ReplyCtx:   "thread-1",
	})
}

// turn runs one whole typed turn so the session has a live agent.
func (h *signoffHarness) turn(t *testing.T, id, text, reply string) {
	t.Helper()
	h.say(id, text)
	if got := h.agent.nextPrompt(t); got != text {
		t.Fatalf("prompt = %q, want %q", got, text)
	}
	h.agent.answer(reply)
	h.p.waitFor(t, "final:"+reply)
	h.waitIdle(t)
}

func (h *signoffHarness) session() *Session {
	return h.e.sessions.GetOrCreateActive(signoffTestKey)
}

func (h *signoffHarness) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.session().Busy() {
		if time.Now().After(deadline) {
			t.Fatal("session stayed busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *signoffHarness) request(t *testing.T, id string) {
	t.Helper()
	target, ok := h.e.signoffTarget(signoffTestKey)
	if !ok {
		t.Fatal("no live session to request a card for")
	}
	h.e.acceptSignoff(target, signoffTestRequest(signoffTestKey, id))
}

func (h *signoffHarness) setExec(command string) {
	h.e.SetSignoffExec(command)
}

func (h *signoffHarness) openCards() int {
	h.e.signoff.mu.Lock()
	defer h.e.signoff.mu.Unlock()
	return len(h.e.signoff.open)
}

func skipSignoffShellOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("signoff_exec tests use sh")
	}
}

// --- posting ---

func TestSignoffCardPostsDirectlyUnderTheTurnsFinalReply(t *testing.T) {
	h := newSignoffHarness(t)
	h.turn(t, "m0", "hello", "hi")

	h.say("m1", "go live")
	h.agent.nextPrompt(t)
	h.request(t, "r1")
	h.say("m2", "and then?")
	h.p.waitFor(t, "msg:"+h.e.i18n.T(MsgMessageQueued))
	if h.p.has("card:r1") {
		t.Fatal("card posted before the turn's final reply")
	}
	h.agent.answer("ready to publish")

	if got := h.agent.nextPrompt(t); got != "and then?" {
		t.Fatalf("next prompt = %q, want the queued message", got)
	}
	h.agent.answer("later")
	final := h.p.waitFor(t, "final:ready to publish")
	card := h.p.waitFor(t, "card:r1")
	next := h.p.waitFor(t, "final:later")
	if card != final+1 || next < card {
		t.Fatalf("card at %d, final reply at %d, next reply at %d; log %q", card, final, next, h.p.entries())
	}
}

func TestSignoffCardOnAnIdleSessionPostsAtOnce(t *testing.T) {
	h := newSignoffHarness(t)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	h.p.waitFor(t, "card:r1")
}

func TestSignoffCardWhileTheSessionIsHeldPostsAtTheUnlock(t *testing.T) {
	h := newSignoffHarness(t)
	h.turn(t, "m0", "hello", "hi")
	session := h.session()
	if !session.TryLock() {
		t.Fatal("could not hold the session")
	}
	h.request(t, "r1")
	time.Sleep(50 * time.Millisecond)
	if h.p.has("card:r1") {
		t.Fatal("card posted while the session was held")
	}
	session.Unlock()
	h.p.waitFor(t, "card:r1")
}

func TestSignoffCardAfterATurnWithNoFinalReplyPostsAtTheUnlock(t *testing.T) {
	h := newSignoffHarness(t)
	h.turn(t, "m0", "hello", "hi")
	h.say("m1", "go live")
	h.agent.nextPrompt(t)
	h.request(t, "r1")
	h.agent.events <- Event{Type: EventError, Error: errors.New("boom")}
	h.p.waitFor(t, "card:r1")
}

func TestSignoffNewerCardReplacesTheOlderOneInTheThread(t *testing.T) {
	h := newSignoffHarness(t)
	h.turn(t, "m0", "hello", "hi")
	h.say("m1", "go live")
	h.agent.nextPrompt(t)
	h.request(t, "r1")
	h.request(t, "r2")
	h.agent.answer("two cards")

	first := h.p.waitFor(t, "card:r1")
	second := h.p.waitFor(t, "card:r2")
	replaced := h.p.waitFor(t, "redraw:r1:replaced:")
	if first >= second || second >= replaced {
		t.Fatalf("log order %q", h.p.entries())
	}
	if got := h.agent.nextPrompt(t); got != "Signoff r1: replaced by r2" {
		t.Fatalf("report = %q", got)
	}
	h.agent.answer("noted")
	h.p.waitFor(t, "final:noted")
	if n := h.openCards(); n != 1 {
		t.Fatalf("open cards = %d, want only the newer one", n)
	}

	// A click on the replaced card does nothing.
	h.p.handle(t, "r1").approve("Lovely Editor")
	time.Sleep(50 * time.Millisecond)
	if h.p.has("redraw:r1:approved:Lovely Editor") {
		t.Fatal("a click on a replaced card was taken")
	}
}

func TestSignoffFailedPostLeavesTheOlderCardOpen(t *testing.T) {
	h := newSignoffHarness(t)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	h.p.waitFor(t, "card:r1")

	h.p.mu.Lock()
	h.p.failPost = true
	h.p.mu.Unlock()
	h.request(t, "r2")
	time.Sleep(100 * time.Millisecond)
	if h.p.has("redraw:r1:replaced:") {
		t.Fatal("older card replaced by a card that never went out")
	}
	if n := h.openCards(); n != 1 {
		t.Fatalf("open cards = %d, want 1", n)
	}
	h.agent.noPrompt(t, 50*time.Millisecond)
}

func TestSignoffNewerCardInAnotherThreadLeavesThisOneOpen(t *testing.T) {
	h := newSignoffHarness(t)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	h.p.waitFor(t, "card:r1")

	other := "test:thread-2"
	h.e.interactiveMu.Lock()
	h.e.interactiveStates[other] = &interactiveState{agentSession: newSignoffTestSession(), platform: h.p, replyCtx: "thread-2"}
	h.e.interactiveMu.Unlock()
	target, ok := h.e.signoffTarget(other)
	if !ok {
		t.Fatal("no live session in the other thread")
	}
	h.e.acceptSignoff(target, signoffTestRequest(other, "r2"))
	h.p.waitFor(t, "card:r2")
	time.Sleep(50 * time.Millisecond)
	if h.p.has("redraw:r1:replaced:") {
		t.Fatal("a card in another thread replaced this one")
	}
	if n := h.openCards(); n != 2 {
		t.Fatalf("open cards = %d, want 2", n)
	}
}

func TestSignoffCardStaysClickableAfterTheSessionStateIsCleared(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	h.setExec(`echo '{"outcome":"refused"}'`)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	card := h.p.handle(t, "r1")

	h.e.cleanupInteractiveState(signoffTestKey)
	card.approve("Lovely Editor")
	h.p.waitFor(t, "redraw:r1:approved:Lovely Editor")
	if got := h.agent.nextPrompt(t); got != "Signoff r1: refused" {
		t.Fatalf("report = %q", got)
	}
}

// --- click, hold, report ---

func TestSignoffApproveRunsTheCommandAndReportsItsOutcome(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin.json")
	runs := filepath.Join(dir, "runs")
	h.setExec(`cat > ` + stdin + `; echo run >> ` + runs + `; echo '{"outcome":"published","message":"5 changes live"}'`)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	card := h.p.handle(t, "r1")

	card.approve("Lovely Editor")
	redraw := h.p.waitFor(t, "redraw:r1:approved:Lovely Editor")

	report := h.agent.nextPrompt(t)
	if report != "Signoff r1: published\n5 changes live" {
		t.Fatalf("report = %q", report)
	}
	if strings.Contains(report, "nonce-r1") {
		t.Fatal("the report carries the nonce")
	}
	got, err := os.ReadFile(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"request_id":"r1","nonce":"nonce-r1","clicked_by":{"id":"aad-Lovely Editor","name":"Lovely Editor"}}`; string(got) != want {
		t.Fatalf("stdin = %s, want %s", got, want)
	}

	h.agent.answer("published, thanks")
	h.p.waitFor(t, "final:published, thanks")
	log := h.p.entries()
	var streams []int
	for i, entry := range log {
		if entry == "stream" && i > redraw {
			streams = append(streams, i)
		}
	}
	if len(streams) != 1 {
		t.Fatalf("streaming cards after the click = %d, want the one placeholder; log %q", len(streams), log)
	}

	// A second click runs nothing.
	card.approve("Lovely Editor")
	time.Sleep(100 * time.Millisecond)
	data, _ := os.ReadFile(runs)
	if n := strings.Count(string(data), "run"); n != 1 {
		t.Fatalf("command ran %d times, want 1", n)
	}
	redraws := 0
	for _, entry := range h.p.entries() {
		if strings.HasPrefix(entry, "redraw:r1:") {
			redraws++
		}
	}
	if redraws != 1 {
		t.Fatalf("card redrawn %d times, want 1", redraws)
	}
	h.agent.noPrompt(t, 50*time.Millisecond)
}

func TestSignoffClickWithAWrongNonceRunsNothing(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	runs := filepath.Join(t.TempDir(), "runs")
	h.setExec(`echo run >> ` + runs + `; echo '{"outcome":"published"}'`)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	card := h.p.handle(t, "r1")

	card.onClick("signoff:r1:wrong", SignoffClicker{ID: "aad-1", Name: "Mallory"})
	card.onClick("signoff:r1:wrong:cancel", SignoffClicker{ID: "aad-1", Name: "Mallory"})
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(runs); err == nil {
		t.Fatal("the command ran")
	}
	if n := h.openCards(); n != 1 {
		t.Fatalf("open cards = %d, want the card still open", n)
	}
	h.agent.noPrompt(t, 50*time.Millisecond)
}

func TestSignoffTypedKeywordsLeaveTheCardOpen(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	runs := filepath.Join(t.TempDir(), "runs")
	h.setExec(`echo run >> ` + runs + `; echo '{"outcome":"published"}'`)
	h.turn(t, "m0", "hello", "hi")
	h.e.interactiveMu.Lock()
	state := h.e.interactiveStates[signoffTestKey]
	h.e.interactiveMu.Unlock()
	state.mu.Lock()
	state.approveAll = true
	state.mu.Unlock()
	h.request(t, "r1")
	h.p.waitFor(t, "card:r1")

	for i, word := range []string{"approve", "ok", "allow all", "确认", "allow"} {
		h.say(fmt.Sprintf("k%d", i), word)
		if got := h.agent.nextPrompt(t); got != word {
			t.Fatalf("prompt = %q, want %q", got, word)
		}
		reply := "reply " + word
		h.agent.answer(reply)
		h.p.waitFor(t, "final:"+reply)
		h.waitIdle(t)
	}
	if _, err := os.Stat(runs); err == nil {
		t.Fatal("the command ran")
	}
	if n := h.openCards(); n != 1 {
		t.Fatalf("open cards = %d, want the card still open", n)
	}
	for _, entry := range h.p.entries() {
		if strings.HasPrefix(entry, "redraw:r1:") {
			t.Fatalf("card redrawn: %q", entry)
		}
	}
}

func TestSignoffCancelReportsWithoutRunningTheCommand(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	runs := filepath.Join(t.TempDir(), "runs")
	h.setExec(`echo run >> ` + runs + `; echo '{"outcome":"published"}'`)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	h.p.handle(t, "r1").cancel("Lovely Editor")

	h.p.waitFor(t, "redraw:r1:cancelled:Lovely Editor")
	if got := h.agent.nextPrompt(t); got != "Signoff r1: cancelled" {
		t.Fatalf("report = %q", got)
	}
	if _, err := os.Stat(runs); err == nil {
		t.Fatal("the command ran on Cancel")
	}
}

func TestSignoffUnsetCommandReportsFailed(t *testing.T) {
	h := newSignoffHarness(t)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	h.p.handle(t, "r1").approve("Lovely Editor")
	if got := h.agent.nextPrompt(t); got != "Signoff r1: failed" {
		t.Fatalf("report = %q", got)
	}
}

func TestSignoffMessageTypedAfterTheClickWaitsForTheReport(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	h.setExec(`echo '{"outcome":"published","message":"live"}'`)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	card := h.p.handle(t, "r1")

	h.say("m1", "still working?")
	h.agent.nextPrompt(t)
	card.approve("Lovely Editor")
	h.p.waitFor(t, "redraw:r1:approved:Lovely Editor")
	h.say("m2", "is it live?")
	h.p.waitFor(t, "msg:"+h.e.i18n.T(MsgMessageQueued))
	h.agent.answer("yes")

	if got := h.agent.nextPrompt(t); got != "Signoff r1: published\nlive" {
		t.Fatalf("prompt after the running turn = %q, want the report", got)
	}
	h.agent.answer("it is live")
	if got := h.agent.nextPrompt(t); got != "is it live?" {
		t.Fatalf("prompt after the report = %q, want the queued message", got)
	}
	h.agent.answer("see above")
	h.p.waitFor(t, "final:see above")
}

func TestSignoffHoldBlocksTypedTurnsAndPsUntilTheReport(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	release := filepath.Join(t.TempDir(), "release")
	h.setExec(`while [ ! -f ` + release + ` ]; do sleep 0.02; done; echo '{"outcome":"published"}'`)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	h.p.handle(t, "r1").approve("Lovely Editor")
	h.p.waitFor(t, "redraw:r1:approved:Lovely Editor")

	h.say("m1", "/ps sneak in")
	h.p.waitFor(t, "msg:"+h.e.i18n.T(MsgPsNoSession))
	h.say("m2", "hello again")
	h.p.waitFor(t, "msg:"+h.e.i18n.T(MsgMessageQueued))
	h.agent.noPrompt(t, 100*time.Millisecond)

	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := h.agent.nextPrompt(t); got != "Signoff r1: published" {
		t.Fatalf("prompt = %q, want the report", got)
	}
	h.agent.answer("done")
	if got := h.agent.nextPrompt(t); got != "hello again" {
		t.Fatalf("prompt = %q, want the queued message", got)
	}
	h.agent.answer("hi again")
	h.p.waitFor(t, "final:hi again")
}

func TestSignoffReportRunsOnTheNewSessionAfterNew(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	release := filepath.Join(t.TempDir(), "release")
	h.setExec(`while [ ! -f ` + release + ` ]; do sleep 0.02; done; echo '{"outcome":"published"}'`)
	h.turn(t, "m0", "hello", "hi")
	old := h.session()
	h.request(t, "r1")
	h.p.handle(t, "r1").approve("Lovely Editor")
	h.p.waitFor(t, "redraw:r1:approved:Lovely Editor")

	h.say("m1", "/new")
	h.p.waitFor(t, "msg:"+h.e.i18n.T(MsgNewSessionCreated))
	fresh := h.session()
	if fresh == old {
		t.Fatal("/new kept the session")
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := h.agent.nextPrompt(t); got != "Signoff r1: published" {
		t.Fatalf("prompt = %q, want the report", got)
	}
	if !fresh.Busy() {
		t.Fatal("the report turn does not hold the new active session")
	}
	if old.Busy() {
		t.Fatal("the old session is still held")
	}
	h.agent.answer("done")
	h.p.waitFor(t, "final:done")
}

func TestSignoffStopBeforeTheReportFinalizesThePlaceholderEmpty(t *testing.T) {
	skipSignoffShellOnWindows(t)
	h := newSignoffHarness(t)
	h.setExec(`sleep 5; echo '{"outcome":"published"}'`)
	h.turn(t, "m0", "hello", "hi")
	h.request(t, "r1")
	h.p.handle(t, "r1").approve("Lovely Editor")
	h.p.waitFor(t, "redraw:r1:approved:Lovely Editor")

	_ = h.e.Stop()
	h.p.waitFor(t, "final:")
	h.agent.noPrompt(t, 50*time.Millisecond)
}
