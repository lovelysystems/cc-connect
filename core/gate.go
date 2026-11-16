package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

const gateTurnLimit = 15 * time.Minute

// GateCard is a confirmation card posted into a session's thread by a process
// outside the agent. Its buttons resolve on the platform side and never reach
// the agent.
type GateCard struct {
	RequestID    string
	Nonce        string
	Title        string
	Body         string
	ConfirmLabel string
}

// GateCardPoster is implemented by platforms that can post a gate card and
// resolve its button clicks. onOutcome receives a fixed-text report once the
// card is resolved; the engine runs it as a turn in the card's session.
type GateCardPoster interface {
	PostGateCard(ctx context.Context, sessionKey string, replyCtx any, card GateCard, onOutcome func(report string)) error
}

// PostGateCard posts a confirm card (or, for kind "notice", plain text) into
// the thread of sessionKey. While a turn holds the session, the confirm card is
// held and posted in the background right after that turn's final reply has
// been sent, or once the session is released; a failure there is only logged.
func (e *Engine) PostGateCard(ctx context.Context, sessionKey, kind string, card GateCard) error {
	platformName, _, _ := strings.Cut(sessionKey, ":")
	var p Platform
	for _, candidate := range e.platforms {
		if candidate.Name() == platformName {
			p = candidate
			break
		}
	}
	if p == nil {
		return fmt.Errorf("gate: platform %q not found for session %q", platformName, sessionKey)
	}
	rc, ok := p.(ReplyContextReconstructor)
	if !ok {
		return fmt.Errorf("gate: platform %q cannot post proactively", platformName)
	}
	replyCtx, err := rc.ReconstructReplyCtx(sessionKey)
	if err != nil {
		return fmt.Errorf("gate: reconstruct reply context: %w", err)
	}

	if kind == "notice" {
		return p.Send(ctx, replyCtx, fmt.Sprintf("**%s**\n\n%s", card.Title, card.Body))
	}

	poster, ok := p.(GateCardPoster)
	if !ok {
		return fmt.Errorf("gate: platform %q cannot post gate cards", platformName)
	}
	onOutcome := func(report string) {
		go func() {
			silent := true
			job := &TimerJob{
				ID:          "gate-" + card.RequestID,
				SessionKey:  sessionKey,
				Prompt:      report,
				Silent:      &silent,
				SessionMode: "reuse",
				CreatedAt:   time.Now(),
			}
			if err := e.ExecuteTimerJob(job); err != nil {
				slog.Error("gate: outcome turn failed", "session", sessionKey, "request_id", card.RequestID, "error", err)
			}
		}()
	}
	post := func() {
		if err := poster.PostGateCard(context.Background(), sessionKey, replyCtx, card, onOutcome); err != nil {
			slog.Error("gate: post card failed", "session", sessionKey, "request_id", card.RequestID, "error", err)
		}
	}
	_, sessions := e.sessionContextForKey(sessionKey)
	session := sessions.FindByID(sessions.ActiveSessionID(sessionKey))
	if session == nil {
		go post()
		return nil
	}
	e.holdGateCard(session, &heldGateCard{post: post}, func() {
		slog.Error("gate: turn still running, card not posted", "session", sessionKey, "request_id", card.RequestID)
	})
	return nil
}

// heldGateCard is a confirm card waiting for the turn that holds its session.
// Whichever of final reply, session release or time limit comes first claims it.
type heldGateCard struct {
	post    func()
	claimed atomic.Bool
}

func (h *heldGateCard) claim() bool { return h.claimed.CompareAndSwap(false, true) }

// holdGateCard keeps h until postHeldGateCards runs for session, the session is
// released, or gateTurnLimit passes; on the time limit h is dropped and
// onDrop runs. A session no turn holds gets the card at once.
func (e *Engine) holdGateCard(session *Session, h *heldGateCard, onDrop func()) {
	released := session.Released()
	e.gateMu.Lock()
	if e.gateHeld == nil {
		e.gateHeld = make(map[*Session][]*heldGateCard)
	}
	e.gateHeld[session] = append(e.gateHeld[session], h)
	e.gateMu.Unlock()

	go func() {
		limit := time.NewTimer(gateTurnLimit)
		defer limit.Stop()
		select {
		case <-released:
			if h.claim() {
				h.post()
			}
		case <-limit.C:
			if h.claim() {
				onDrop()
			}
		}
		e.gateMu.Lock()
		defer e.gateMu.Unlock()
		held := e.gateHeld[session]
		for i, candidate := range held {
			if candidate == h {
				held = append(held[:i], held[i+1:]...)
				break
			}
		}
		if len(held) == 0 {
			delete(e.gateHeld, session)
		} else {
			e.gateHeld[session] = held
		}
	}()
}

// postHeldGateCards posts the cards held for session. The turn calls it right
// after its final reply has been sent.
func (e *Engine) postHeldGateCards(session *Session) {
	e.gateMu.Lock()
	held := e.gateHeld[session]
	delete(e.gateHeld, session)
	e.gateMu.Unlock()
	for _, h := range held {
		if h.claim() {
			go h.post()
		}
	}
}
