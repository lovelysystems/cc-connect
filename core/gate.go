package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	gateTurnPoll  = 500 * time.Millisecond
	gateTurnLimit = 15 * time.Minute
)

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
// the thread of sessionKey. A confirm card is posted in the background once the
// session's running turn has ended; a failure there is only logged.
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
	go func() {
		if !e.waitTurnEnd(sessionKey) {
			slog.Error("gate: turn still running, card not posted", "session", sessionKey, "request_id", card.RequestID)
			return
		}
		if err := poster.PostGateCard(context.Background(), sessionKey, replyCtx, card, onOutcome); err != nil {
			slog.Error("gate: post card failed", "session", sessionKey, "request_id", card.RequestID, "error", err)
		}
	}()
	return nil
}

// waitTurnEnd blocks until the session of sessionKey is no longer busy, so the
// card lands below the reply of the turn that asked for it. It reports false
// when the turn is still running after gateTurnLimit.
func (e *Engine) waitTurnEnd(sessionKey string) bool {
	_, sessions := e.sessionContextForKey(sessionKey)
	session := sessions.FindByID(sessions.ActiveSessionID(sessionKey))
	if session == nil {
		return true
	}
	deadline := time.Now().Add(gateTurnLimit)
	for session.Busy() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(gateTurnPoll)
	}
	return true
}
