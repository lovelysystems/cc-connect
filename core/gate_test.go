package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gatePlatform records sends and posted gate cards in one ordered list.
type gatePlatform struct {
	stubPlatformEngine
	posted  chan struct{}
	onClick GateClick
}

func newGatePlatform() *gatePlatform {
	return &gatePlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, posted: make(chan struct{}, 4)}
}

func (p *gatePlatform) ReconstructReplyCtx(string) (any, error) { return "ctx", nil }

func (p *gatePlatform) PostGateCard(_ context.Context, _ string, _ any, card GateCard, onClick GateClick) error {
	p.mu.Lock()
	p.sent = append(p.sent, "card:"+card.Title)
	p.onClick = onClick
	p.mu.Unlock()
	p.posted <- struct{}{}
	return nil
}

func indexOfSent(sent []string, needle string) int {
	for i, s := range sent {
		if strings.Contains(s, needle) {
			return i
		}
	}
	return -1
}

func TestPostGateCard_PostsRightAfterTheTurnsFinalReply(t *testing.T) {
	p := newGatePlatform()
	as := newControllableSession("s1")
	agent := &controllableAgent{nextSession: as}
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	key := "test:chat:user1"
	session := e.sessions.GetOrCreateActive(key)
	if !session.TryLock() {
		t.Fatal("session already locked")
	}

	done := make(chan struct{})
	go func() {
		e.processInteractiveMessageWith(p, &Message{
			Platform: "test", SessionKey: key, UserID: "u1", Content: "first", ReplyCtx: "ctx",
		}, session, agent, e.sessions, key, "", "")
		close(done)
	}()

	var state *interactiveState
	deadline := time.Now().Add(2 * time.Second)
	for state == nil {
		if time.Now().After(deadline) {
			t.Fatal("turn did not start")
		}
		e.interactiveMu.Lock()
		state = e.interactiveStates[key]
		e.interactiveMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}

	if err := e.PostGateCard(context.Background(), key, "confirm", GateCard{RequestID: "r1", Title: "Publish?"}); err != nil {
		t.Fatalf("PostGateCard: %v", err)
	}

	// A queued message keeps the session busy after the first reply, so a card
	// posted now can only come from the final-reply point.
	state.mu.Lock()
	state.pendingMessages = append(state.pendingMessages, queuedMessage{
		messageID: "m2", platform: p, replyCtx: "ctx", content: "second", msgPlatform: "test", msgSessionKey: key,
	})
	state.mu.Unlock()

	select {
	case <-p.posted:
		t.Fatal("card posted before the turn's final reply")
	case <-time.After(100 * time.Millisecond):
	}

	as.events <- Event{Type: EventResult, Content: "first reply", Done: true}

	select {
	case <-p.posted:
	case <-time.After(2 * time.Second):
		t.Fatal("card not posted after the turn's final reply")
	}
	if !session.Busy() {
		t.Fatal("expected the queued turn to still hold the session")
	}
	sent := p.getSent()
	reply, card := indexOfSent(sent, "first reply"), indexOfSent(sent, "card:Publish?")
	if reply < 0 || card < reply {
		t.Fatalf("sent = %q, want the reply before the card", sent)
	}

	as.events <- Event{Type: EventResult, Content: "second reply", Done: true}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("queued turn did not finish")
	}
}

func TestPostGateCard_PostsWhenSessionIsReleasedWithoutAReply(t *testing.T) {
	p := newGatePlatform()
	e := NewEngine("test", &controllableAgent{}, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	key := "test:chat:user1"
	session := e.sessions.GetOrCreateActive(key)
	if !session.TryLock() {
		t.Fatal("session already locked")
	}

	if err := e.PostGateCard(context.Background(), key, "confirm", GateCard{RequestID: "r1", Title: "Publish?"}); err != nil {
		t.Fatalf("PostGateCard: %v", err)
	}
	select {
	case <-p.posted:
		t.Fatal("card posted while the turn is running")
	case <-time.After(100 * time.Millisecond):
	}

	session.Unlock()
	select {
	case <-p.posted:
	case <-time.After(2 * time.Second):
		t.Fatal("card not posted after the session was released")
	}
}

// gateStreamPlatform is a gatePlatform whose turns stream into cards; it counts
// the cards a turn creates itself.
type gateStreamPlatform struct {
	*gatePlatform
	created atomic.Int32
}

func (p *gateStreamPlatform) CreateStreamingCard(context.Context, any) (StreamingCard, error) {
	p.created.Add(1)
	return &recordingStreamCard{}, nil
}

// sendSignalSession passes each prompt a turn hands to the agent to sent.
type sendSignalSession struct {
	*controllableAgentSession
	sent chan string
}

func (s *sendSignalSession) Send(prompt, msgID string, images []ImageAttachment, files []FileAttachment) error {
	s.sent <- prompt
	return s.controllableAgentSession.Send(prompt, msgID, images, files)
}

// postGateCardForClick posts a gate card into an idle session and returns
// the click callback the platform received.
func postGateCardForClick(t *testing.T, e *Engine, p *gateStreamPlatform, key string) GateClick {
	t.Helper()
	if err := e.PostGateCard(context.Background(), key, "confirm", GateCard{RequestID: "r1", Title: "Publish?"}); err != nil {
		t.Fatalf("PostGateCard: %v", err)
	}
	select {
	case <-p.posted:
	case <-time.After(2 * time.Second):
		t.Fatal("card not posted")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.onClick
}

func waitFinalized(t *testing.T, card *recordingStreamCard) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !card.finalized() {
		if time.Now().After(deadline) {
			t.Fatal("placeholder was never finalized")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPostGateCard_OutcomeTurnFillsThePostedPlaceholder(t *testing.T) {
	p := &gateStreamPlatform{gatePlatform: newGatePlatform()}
	as := &sendSignalSession{controllableAgentSession: newControllableSession("s1"), sent: make(chan string, 1)}
	agent := &controllableAgent{nextSession: as}
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	key := "test:chat:user1"
	onOutcome := postGateCardForClick(t, e, p, key)()

	placeholder := &recordingStreamCard{}
	onOutcome(`Gate report for request r1: outcome "published"`, placeholder)
	select {
	case <-as.sent:
	case <-time.After(2 * time.Second):
		t.Fatal("outcome turn did not start")
	}
	as.events <- Event{Type: EventResult, Content: "Published, all good.", Done: true}

	waitFinalized(t, placeholder)
	if !strings.Contains(placeholder.finalContent(), "Published, all good.") {
		t.Errorf("placeholder content = %q", placeholder.finalContent())
	}
	if n := p.created.Load(); n != 0 {
		t.Errorf("outcome turn created %d streaming cards of its own", n)
	}
}

func TestPostGateCard_FailedOutcomeTurnClosesThePlaceholderAndReleasesTheSession(t *testing.T) {
	p := &gateStreamPlatform{gatePlatform: newGatePlatform()}
	agent := &controllableAgent{startSessionFn: func(context.Context, string) (AgentSession, error) {
		return nil, errors.New("agent did not start")
	}}
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	key := "test:chat:user1"
	onOutcome := postGateCardForClick(t, e, p, key)()

	placeholder := &recordingStreamCard{}
	onOutcome(`Gate report for request r1: outcome "published"`, placeholder)

	waitFinalized(t, placeholder)
	if placeholder.finalContent() != gateOutcomeFailedText {
		t.Errorf("placeholder content = %q", placeholder.finalContent())
	}
	waitReleased(t, e.sessions.GetOrCreateActive(key))
}

func waitPrompt(t *testing.T, sent <-chan string) string {
	t.Helper()
	select {
	case prompt := <-sent:
		return prompt
	case <-time.After(2 * time.Second):
		t.Fatal("no prompt reached the agent")
		return ""
	}
}

func waitReleased(t *testing.T, session *Session) {
	t.Helper()
	select {
	case <-session.Released():
	case <-time.After(2 * time.Second):
		t.Fatal("session was never released")
	}
}

func TestGateClick_MessageWhileTheClickHoldsTheSessionIsAnsweredAfterTheOutcome(t *testing.T) {
	p := &gateStreamPlatform{gatePlatform: newGatePlatform()}
	as := &sendSignalSession{controllableAgentSession: newControllableSession("s1"), sent: make(chan string, 4)}
	agent := &controllableAgent{nextSession: as}
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	key := "test:chat:user1"
	onOutcome := postGateCardForClick(t, e, p, key)()
	session := e.sessions.GetOrCreateActive(key)
	if !session.Busy() {
		t.Fatal("the click must hold the session")
	}

	// No agent process runs for the session, as after an idle reap.
	e.handleMessage(p, &Message{Platform: "test", SessionKey: key, UserID: "u1", UserName: "user", Content: "typed meanwhile", ReplyCtx: "ctx"})
	if indexOfSent(p.getSent(), e.i18n.T(MsgMessageQueued)) < 0 {
		t.Fatalf("message not acknowledged as queued, sent = %q", p.getSent())
	}
	select {
	case prompt := <-as.sent:
		t.Fatalf("prompt reached the agent while the click holds the session: %q", prompt)
	case <-time.After(100 * time.Millisecond):
	}

	placeholder := &recordingStreamCard{}
	onOutcome(`Gate report for request r1: outcome "published"`, placeholder)
	if prompt := waitPrompt(t, as.sent); !strings.Contains(prompt, "Gate report") {
		t.Fatalf("first prompt = %q, want the gate report", prompt)
	}
	as.events <- Event{Type: EventResult, Content: "Published, all good.", Done: true}

	if prompt := waitPrompt(t, as.sent); !strings.Contains(prompt, "typed meanwhile") {
		t.Fatalf("second prompt = %q, want the queued message", prompt)
	}
	waitFinalized(t, placeholder)
	if !strings.Contains(placeholder.finalContent(), "Published, all good.") {
		t.Errorf("placeholder content = %q", placeholder.finalContent())
	}
	as.events <- Event{Type: EventResult, Content: "Answer to the typed message.", Done: true}
	waitReleased(t, session)
}

func TestGateClick_WhileATurnRunsTheOutcomeTurnFollowsIt(t *testing.T) {
	p := &gateStreamPlatform{gatePlatform: newGatePlatform()}
	as := &sendSignalSession{controllableAgentSession: newControllableSession("s1"), sent: make(chan string, 4)}
	agent := &controllableAgent{nextSession: as}
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	key := "test:chat:user1"
	onClick := postGateCardForClick(t, e, p, key)

	session := e.sessions.GetOrCreateActive(key)
	if !session.TryLock() {
		t.Fatal("session already locked")
	}
	go e.processInteractiveMessageWith(p, &Message{
		Platform: "test", SessionKey: key, UserID: "u1", Content: "running turn", ReplyCtx: "ctx",
	}, session, agent, e.sessions, key, "", "")
	if prompt := waitPrompt(t, as.sent); !strings.Contains(prompt, "running turn") {
		t.Fatalf("first prompt = %q", prompt)
	}

	resolved := make(chan struct{})
	placeholder := &recordingStreamCard{}
	go func() {
		onClick()(`Gate report for request r1: outcome "published"`, placeholder)
		close(resolved)
	}()
	select {
	case <-resolved:
	case <-time.After(2 * time.Second):
		t.Fatal("the click must not wait for the running turn")
	}
	select {
	case prompt := <-as.sent:
		t.Fatalf("outcome turn started while another turn runs: %q", prompt)
	case <-time.After(100 * time.Millisecond):
	}

	as.events <- Event{Type: EventResult, Content: "running reply", Done: true}
	if prompt := waitPrompt(t, as.sent); !strings.Contains(prompt, "Gate report") {
		t.Fatalf("second prompt = %q, want the gate report", prompt)
	}
	as.events <- Event{Type: EventResult, Content: "Published, all good.", Done: true}
	waitFinalized(t, placeholder)
	if !strings.Contains(placeholder.finalContent(), "Published, all good.") {
		t.Errorf("placeholder content = %q", placeholder.finalContent())
	}
	waitReleased(t, session)
}
