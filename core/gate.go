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

// gateOutcomeFailedText replaces the placeholder when the outcome turn leaves it unfilled.
const gateOutcomeFailedText = "No answer to the gate outcome."

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

// GateClick is called by the platform when a gate card's button is clicked,
// before the card changes or the gate command runs. It holds the card's session
// until the outcome turn is done and returns onOutcome, which the platform must
// call exactly once with a fixed-text report; the engine runs it as a turn in
// that session. A non-nil placeholder is a loading card already posted for that
// turn: the turn streams its answer into it.
type GateClick func() (onOutcome func(report string, placeholder StreamingCard))

// GateCardPoster is implemented by platforms that can post a gate card and
// resolve its button clicks.
type GateCardPoster interface {
	PostGateCard(ctx context.Context, sessionKey string, replyCtx any, card GateCard, onClick GateClick) error
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
	onClick := func() func(string, StreamingCard) {
		locked := e.lockGateSession(sessionKey)
		return func(report string, placeholder StreamingCard) {
			go func() {
				silent := true
				job := &TimerJob{
					ID:          "gate-" + card.RequestID,
					SessionKey:  sessionKey,
					Prompt:      report,
					Silent:      &silent,
					SessionMode: "reuse",
					CreatedAt:   time.Now(),
					HeldSession: <-locked,
				}
				var tracked *finalizeTracker
				if placeholder != nil {
					tracked = &finalizeTracker{StreamingCard: placeholder}
					job.Placeholder = tracked
				}
				if err := e.ExecuteTimerJob(job); err != nil {
					slog.Error("gate: outcome turn failed", "session", sessionKey, "request_id", card.RequestID, "error", err)
				}
				if tracked != nil && !tracked.finalized.Load() {
					if err := tracked.Finalize(context.Background(), gateOutcomeFailedText); err != nil {
						slog.Error("gate: placeholder edit failed", "session", sessionKey, "request_id", card.RequestID, "error", err)
					}
				}
			}()
		}
	}
	post := func() {
		if err := poster.PostGateCard(context.Background(), sessionKey, replyCtx, card, onClick); err != nil {
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

// lockGateSession locks the active session of sessionKey for a clicked gate
// card and yields it on the returned channel. While a turn holds the session,
// the lock is taken as soon as that turn releases it; messages arriving in the
// meantime queue for the busy session.
func (e *Engine) lockGateSession(sessionKey string) <-chan *Session {
	_, sessions := e.sessionContextForKey(sessionKey)
	session := sessions.GetOrCreateActive(sessionKey)
	locked := make(chan *Session, 1)
	if session.TryLock() {
		locked <- session
		return locked
	}
	go func() {
		for {
			<-session.Released()
			if session.TryLock() {
				locked <- session
				return
			}
		}
	}()
	return locked
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

// finalizeTracker records whether Finalize was called on the card it wraps.
type finalizeTracker struct {
	StreamingCard
	finalized atomic.Bool
}

func (t *finalizeTracker) Finalize(ctx context.Context, content string) error {
	t.finalized.Store(true)
	return t.StreamingCard.Finalize(ctx, content)
}
