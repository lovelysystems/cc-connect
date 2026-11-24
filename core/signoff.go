package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// SignoffValuePrefix starts the value of every signoff card button.
const SignoffValuePrefix = "signoff:"

const signoffCancelSuffix = ":cancel"

// Outcomes of a signoff card, as the agent's report names them.
const (
	SignoffPublished = "published"
	SignoffRefused   = "refused"
	SignoffCancelled = "cancelled"
	SignoffReplaced  = "replaced"
	SignoffFailed    = "failed"
)

// SignoffWithdrawn is the logged outcome of a card the agent took back.
const SignoffWithdrawn = "withdrawn"

// SignoffRequest is the JSON body for POST /signoff/send.
type SignoffRequest struct {
	SessionKey   string        `json:"session_key"`
	RequestID    string        `json:"request_id"`
	Nonce        string        `json:"nonce"`
	Title        string        `json:"title"`
	Body         []SignoffLine `json:"body"`
	ConfirmLabel string        `json:"confirm_label"`
	CancelLabel  string        `json:"cancel_label"`
}

// SignoffLine is one line of a signoff card's body: plain text in a style.
type SignoffLine struct {
	Style CardTextStyle `json:"style"`
	Text  string        `json:"text"`
}

var signoffStyles = map[CardTextStyle]bool{
	CardTextHeading: true,
	CardTextName:    true,
	CardTextDetail:  true,
	CardTextLink:    true,
}

func (r *SignoffRequest) validate() error {
	required := []struct{ name, value string }{
		{"session_key", r.SessionKey},
		{"request_id", r.RequestID},
		{"nonce", r.Nonce},
		{"title", r.Title},
		{"confirm_label", r.ConfirmLabel},
		{"cancel_label", r.CancelLabel},
	}
	for _, f := range required {
		if f.value == "" {
			return fmt.Errorf("%s is required", f.name)
		}
	}
	// The button value is split on ':'.
	if strings.Contains(r.RequestID, ":") || strings.Contains(r.Nonce, ":") {
		return errors.New("request_id and nonce must not contain ':'")
	}
	for _, f := range required {
		if hasControlChar(f.value) {
			return fmt.Errorf("%s holds a control character", f.name)
		}
	}
	if len(r.Body) == 0 {
		return errors.New("body must have at least one line")
	}
	for i, line := range r.Body {
		if !signoffStyles[line.Style] {
			return fmt.Errorf("body[%d]: unknown style %q", i, line.Style)
		}
		if line.Text == "" {
			return fmt.Errorf("body[%d]: text is required", i)
		}
		if hasControlChar(line.Text) {
			return fmt.Errorf("body[%d]: text holds a control character", i)
		}
	}
	return nil
}

// hasControlChar reports whether s holds a control character (U+0000–U+001F,
// U+007F–U+009F), U+2028 or U+2029.
func hasControlChar(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	})
}

func (r *SignoffRequest) confirmValue() string {
	return SignoffValuePrefix + r.RequestID + ":" + r.Nonce
}

func (r *SignoffRequest) cancelValue() string {
	return r.confirmValue() + signoffCancelSuffix
}

// buildSignoffCard builds the card of a validated request. Every word on it
// comes from the request.
func buildSignoffCard(r *SignoffRequest) *Card {
	card := &Card{Header: &CardHeader{Title: r.Title}}
	for _, line := range r.Body {
		card.Elements = append(card.Elements, CardText{Text: line.Text, Style: line.Style})
	}
	card.Elements = append(card.Elements, CardActions{Buttons: []CardButton{
		PrimaryBtn(r.ConfirmLabel, r.confirmValue()),
		DefaultBtn(r.CancelLabel, r.cancelValue()),
	}})
	return card
}

// signoffReport is the text of the turn that tells the agent how a card ended.
func signoffReport(requestID, outcome, detail string) string {
	report := "Signoff " + requestID + ": " + outcome
	if detail != "" {
		report += "\n" + detail
	}
	return report
}

// signoffs holds an engine's signoff cards, in memory only.
type signoffs struct {
	mu sync.Mutex
	// pending cards wait for the turn holding their session to send its final
	// reply or let go of the session.
	pending map[*Session][]*signoffCard
	// posting cards left pending and wait for their post to start or for the
	// platform to answer it.
	posting []*signoffCard
	// open cards are posted and wait for a click or a newer card.
	open []*signoffCard
	// holds are the reservations of report turns that have not started yet,
	// per session.
	holds map[*Session][]<-chan struct{}
	// accepted holds the confirm value of every request accepted in this
	// process, so a request_id and nonce pair draws at most one card.
	accepted map[string]bool

	exec      string        // shell command run on Approve
	execLimit time.Duration // zero means signoffExecLimit
}

type signoffCard struct {
	req        SignoffRequest
	card       *Card
	sessionKey string
	platform   Platform
	replyCtx   any

	// posted is closed once the platform has answered the post; handle is
	// set before that when the post succeeded.
	posted chan struct{}
	// started is set, under signoffs.mu, when the post begins.
	started bool
	handle  SignoffCardHandle
	// resolved is claimed once, by a click, a newer card or a withdraw.
	resolved atomic.Bool
}

// signoffTarget is what the route needs to know about a session that is to
// get a card.
type signoffTarget struct {
	agentPid int
	platform Platform
	replyCtx any
}

// SetSignoffExec sets the shell command run when a signoff card is approved.
func (e *Engine) SetSignoffExec(command string) {
	e.signoff.mu.Lock()
	defer e.signoff.mu.Unlock()
	e.signoff.exec = command
}

// signoffTarget looks up the live agent session of sessionKey. ok is false
// when no agent runs for it.
func (e *Engine) signoffTarget(sessionKey string) (t signoffTarget, ok bool) {
	iKey := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[iKey]
	e.interactiveMu.Unlock()
	if state == nil {
		return t, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.agentSession == nil || !state.agentSession.Alive() || state.platform == nil {
		return t, false
	}
	if reporter, ok := state.agentSession.(AgentProcessReporter); ok {
		t.agentPid = reporter.Pid()
	}
	t.platform = state.platform
	t.replyCtx = state.replyCtx
	return t, true
}

// acceptSignoff takes a validated card for the session t was looked up for.
// The card is posted under the final reply of the turn holding the session,
// or when that turn lets go of the session, or at once when no turn holds it.
// It returns false, and takes nothing, when the request's request_id and nonce
// were already accepted.
func (e *Engine) acceptSignoff(t signoffTarget, req SignoffRequest) bool {
	c := &signoffCard{
		req:        req,
		card:       buildSignoffCard(&req),
		sessionKey: req.SessionKey,
		platform:   t.platform,
		replyCtx:   t.replyCtx,
		posted:     make(chan struct{}),
	}
	_, sessions := e.sessionContextForKey(req.SessionKey)
	session := sessions.GetOrCreateActive(req.SessionKey)
	released := session.Released()

	key := req.confirmValue()
	e.signoff.mu.Lock()
	if e.signoff.accepted[key] {
		e.signoff.mu.Unlock()
		slog.Warn("signoff: request_id and nonce were already used", "session", req.SessionKey, "request_id", req.RequestID)
		return false
	}
	if e.signoff.accepted == nil {
		e.signoff.accepted = make(map[string]bool)
	}
	e.signoff.accepted[key] = true
	if e.signoff.pending == nil {
		e.signoff.pending = make(map[*Session][]*signoffCard)
	}
	e.signoff.pending[session] = append(e.signoff.pending[session], c)
	e.signoff.mu.Unlock()
	slog.Info("signoff: card accepted", "session", req.SessionKey, "request_id", req.RequestID)

	go func() {
		select {
		case <-released:
			e.postSignoffCards(session)
		case <-e.ctx.Done():
		}
	}()
	return true
}

// postSignoffCards posts the cards waiting for session, in the order they
// were requested. The turn calls it right after its final reply went out.
func (e *Engine) postSignoffCards(session *Session) {
	e.signoff.mu.Lock()
	cards := e.signoff.pending[session]
	delete(e.signoff.pending, session)
	e.signoff.posting = append(e.signoff.posting, cards...)
	e.signoff.mu.Unlock()
	for _, c := range cards {
		e.signoff.mu.Lock()
		c.started = slices.Contains(e.signoff.posting, c)
		e.signoff.mu.Unlock()
		if !c.started {
			continue
		}
		e.postSignoffCard(c)
		e.signoff.mu.Lock()
		e.signoff.posting = slices.DeleteFunc(e.signoff.posting, func(p *signoffCard) bool { return p == c })
		e.signoff.mu.Unlock()
	}
}

// postSignoffCard posts c; once it is out, every older open card in the same
// thread is replaced by it.
func (e *Engine) postSignoffCard(c *signoffCard) {
	defer close(c.posted)
	poster, ok := c.platform.(SignoffCardPlatform)
	if !ok {
		slog.Error("signoff: platform cannot post signoff cards", "platform", c.platform.Name(), "session", c.sessionKey, "request_id", c.req.RequestID)
		return
	}
	handle, err := poster.PostSignoffCard(e.ctx, c.replyCtx, c.card, func(value string, by SignoffClicker) (SignoffMark, func()) {
		return e.signoffClicked(c, value, by)
	})
	if err != nil || handle == nil {
		slog.Error("signoff: post card failed", "session", c.sessionKey, "request_id", c.req.RequestID, "error", err)
		return
	}
	c.handle = handle
	slog.Info("signoff: card posted", "session", c.sessionKey, "request_id", c.req.RequestID)

	thread := handle.Thread()
	var older []*signoffCard
	e.signoff.mu.Lock()
	open := e.signoff.open[:0]
	for _, o := range e.signoff.open {
		if o.handle.Thread() == thread {
			older = append(older, o)
		} else {
			open = append(open, o)
		}
	}
	e.signoff.open = append(open, c)
	e.signoff.mu.Unlock()

	for _, o := range older {
		if !o.resolved.CompareAndSwap(false, true) {
			continue
		}
		slog.Info("signoff: outcome", "session", o.sessionKey, "request_id", o.req.RequestID, "outcome", SignoffReplaced)
		if err := o.handle.Redraw(e.ctx, SignoffMark{Kind: SignoffMarkReplaced, At: time.Now()}); err != nil {
			slog.Error("signoff: redraw replaced card failed", "session", o.sessionKey, "request_id", o.req.RequestID, "error", err)
		}
		session, granted := e.reserveSignoffSession(o.sessionKey)
		go e.runSignoffReport(o, session, granted, signoffReport(o.req.RequestID, SignoffReplaced+" by "+c.req.RequestID, ""), nil)
	}
}

// withdrawSignoff takes back the card requestID of sessionKey while it waits
// for a click. A card whose post has not started is never posted; a posted one
// is drawn as replaced. It returns false when no such card waits.
func (e *Engine) withdrawSignoff(sessionKey, requestID string) bool {
	match := func(c *signoffCard) bool { return c.sessionKey == sessionKey && c.req.RequestID == requestID }
	e.signoff.mu.Lock()
	for session, cards := range e.signoff.pending {
		i := slices.IndexFunc(cards, match)
		if i < 0 {
			continue
		}
		if cards = slices.Delete(cards, i, i+1); len(cards) == 0 {
			delete(e.signoff.pending, session)
		} else {
			e.signoff.pending[session] = cards
		}
		e.signoff.mu.Unlock()
		slog.Info("signoff: outcome", "session", sessionKey, "request_id", requestID, "outcome", SignoffWithdrawn)
		return true
	}
	if i := slices.IndexFunc(e.signoff.posting, match); i >= 0 {
		c := e.signoff.posting[i]
		if !c.started {
			e.signoff.posting = slices.Delete(e.signoff.posting, i, i+1)
			e.signoff.mu.Unlock()
			slog.Info("signoff: outcome", "session", sessionKey, "request_id", requestID, "outcome", SignoffWithdrawn)
			return true
		}
		e.signoff.mu.Unlock()
		<-c.posted
		e.signoff.mu.Lock()
	}
	i := slices.IndexFunc(e.signoff.open, match)
	if i < 0 || !e.signoff.open[i].resolved.CompareAndSwap(false, true) {
		e.signoff.mu.Unlock()
		return false
	}
	c := e.signoff.open[i]
	e.signoff.open = slices.Delete(e.signoff.open, i, i+1)
	e.signoff.mu.Unlock()

	slog.Info("signoff: outcome", "session", sessionKey, "request_id", requestID, "outcome", SignoffWithdrawn)
	if err := c.handle.Redraw(e.ctx, SignoffMark{Kind: SignoffMarkReplaced, At: time.Now()}); err != nil {
		slog.Error("signoff: redraw withdrawn card failed", "session", sessionKey, "request_id", requestID, "error", err)
	}
	return true
}

// signoffClicked handles a button press on c. Only a press carrying one of
// c's button values counts, and only the first one. For that press it reserves
// the session and returns the mark the platform draws under the card, and
// resolve, which runs the rest of the click.
func (e *Engine) signoffClicked(c *signoffCard, value string, by SignoffClicker) (SignoffMark, func()) {
	<-c.posted
	approve := value == c.req.confirmValue()
	if !approve && value != c.req.cancelValue() {
		slog.Warn("signoff: click with a value that is not the card's", "session", c.sessionKey, "request_id", c.req.RequestID)
		return SignoffMark{}, nil
	}
	if c.handle == nil || !c.resolved.CompareAndSwap(false, true) {
		return SignoffMark{}, nil
	}
	e.signoff.mu.Lock()
	for i, o := range e.signoff.open {
		if o == c {
			e.signoff.open = append(e.signoff.open[:i], e.signoff.open[i+1:]...)
			break
		}
	}
	e.signoff.mu.Unlock()

	session, granted := e.reserveSignoffSession(c.sessionKey)
	mark := SignoffMark{Kind: SignoffMarkCancelled, By: by.Name, At: time.Now()}
	if approve {
		mark.Kind = SignoffMarkApproved
	}
	slog.Info("signoff: card clicked", "session", c.sessionKey, "request_id", c.req.RequestID, "approve", approve, "clicked_by", by.ID)
	return mark, func() { e.resolveSignoffClick(c, session, granted, approve, by) }
}

// reserveSignoffSession reserves the active session of sessionKey for a
// report turn. Once the reservation holds the session, its agent is not
// closed for idleness.
func (e *Engine) reserveSignoffSession(sessionKey string) (*Session, <-chan struct{}) {
	_, sessions := e.sessionContextForKey(sessionKey)
	session := sessions.GetOrCreateActive(sessionKey)
	granted := session.Reserve()
	e.signoff.mu.Lock()
	if e.signoff.holds == nil {
		e.signoff.holds = make(map[*Session][]<-chan struct{})
	}
	e.signoff.holds[session] = append(e.signoff.holds[session], granted)
	e.signoff.mu.Unlock()
	go func() {
		select {
		case <-granted:
		case <-e.ctx.Done():
			return
		}
		iKey := e.interactiveKeyForSessionKey(sessionKey)
		e.interactiveMu.Lock()
		state := e.interactiveStates[iKey]
		e.interactiveMu.Unlock()
		e.cancelAgentSessionIdleClose(state)
	}()
	return session, granted
}

// dropSignoffHold forgets a reservation once its report turn starts or is
// given up.
func (e *Engine) dropSignoffHold(session *Session, granted <-chan struct{}) {
	e.signoff.mu.Lock()
	defer e.signoff.mu.Unlock()
	holds := e.signoff.holds[session]
	for i, h := range holds {
		if h == granted {
			holds = append(holds[:i], holds[i+1:]...)
			break
		}
	}
	if len(holds) == 0 {
		delete(e.signoff.holds, session)
	} else {
		e.signoff.holds[session] = holds
	}
}

// signoffHoldsIdle reports whether a signoff holds session for a report turn
// that has not started yet.
func (e *Engine) signoffHoldsIdle(session *Session) bool {
	e.signoff.mu.Lock()
	defer e.signoff.mu.Unlock()
	for _, granted := range e.signoff.holds[session] {
		select {
		case <-granted:
			return true
		default:
		}
	}
	return false
}

// resolveSignoffClick posts the placeholder the report turn will write into,
// runs signoff_exec on Approve, and hands the outcome to the agent.
func (e *Engine) resolveSignoffClick(c *signoffCard, session *Session, granted <-chan struct{}, approve bool, by SignoffClicker) {
	var placeholder *signoffPlaceholder
	if scp, ok := c.platform.(StreamingCardPlatform); ok {
		if sc, err := scp.CreateStreamingCard(e.ctx, c.replyCtx); err != nil {
			slog.Warn("signoff: placeholder post failed", "session", c.sessionKey, "request_id", c.req.RequestID, "error", err)
		} else {
			placeholder = &signoffPlaceholder{StreamingCard: sc}
		}
	}
	outcome, detail := SignoffCancelled, ""
	if approve {
		outcome, detail = e.runSignoffExec(c.sessionKey, signoffExecInput{RequestID: c.req.RequestID, Nonce: c.req.Nonce, ClickedBy: by})
	}
	slog.Info("signoff: outcome", "session", c.sessionKey, "request_id", c.req.RequestID, "outcome", outcome)
	var turnPlaceholder StreamingCard
	if placeholder != nil {
		turnPlaceholder = placeholder
		defer func() {
			if placeholder.finalized.Load() {
				return
			}
			if err := placeholder.StreamingCard.Finalize(context.Background(), ""); err != nil {
				slog.Warn("signoff: finalize unused placeholder failed", "session", c.sessionKey, "error", err)
			}
		}()
	}
	e.runSignoffReport(c, session, granted, signoffReport(c.req.RequestID, outcome, detail), turnPlaceholder)
}

// runSignoffReport waits for the reserved session and runs the report turn
// on it. When /new, /cancel or /switch made another session active
// meanwhile, the turn runs on that one instead.
func (e *Engine) runSignoffReport(c *signoffCard, session *Session, granted <-chan struct{}, report string, placeholder StreamingCard) {
	_, sessions := e.sessionContextForKey(c.sessionKey)
	for {
		select {
		case <-granted:
		case <-e.ctx.Done():
			e.dropSignoffHold(session, granted)
			session.CancelReservation(granted)
			return
		}
		active := sessions.GetOrCreateActive(c.sessionKey)
		if active == session {
			break
		}
		e.dropSignoffHold(session, granted)
		session.Unlock()
		session, granted = e.reserveSignoffSession(c.sessionKey)
	}

	agent, sessions := e.sessionContextForKey(c.sessionKey)
	iKey := e.interactiveKeyForSessionKey(c.sessionKey)
	workspaceDir := ""
	if iKey != c.sessionKey {
		workspaceDir = strings.TrimSuffix(iKey, ":"+c.sessionKey)
	}
	msg := &Message{
		SessionKey:   c.sessionKey,
		Platform:     c.platform.Name(),
		Content:      report,
		ReplyCtx:     c.replyCtx,
		Placeholder:  placeholder,
		KeepLanguage: true,
	}
	e.dropSignoffHold(session, granted)
	e.processInteractiveMessageWith(c.platform, msg, session, agent, sessions, iKey, workspaceDir, c.sessionKey)
}

// signoffPlaceholder is the "Typing…" card posted at a click. It records
// whether the report turn finalized it.
type signoffPlaceholder struct {
	StreamingCard
	finalized atomic.Bool
}

func (p *signoffPlaceholder) Finalize(ctx context.Context, content string) error {
	p.finalized.Store(true)
	return p.StreamingCard.Finalize(ctx, content)
}
