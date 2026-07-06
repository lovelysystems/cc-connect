package teams

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	"github.com/golang-jwt/jwt/v5"
)

// maxBodyBytes caps the activity payload read from the connector.
const maxBodyBytes = 1 << 20 // 1 MiB

// handleActivity is the Bot Connector webhook entry point. It authenticates the
// request before reading the body, then hands the activity to the engine.
func (p *Platform) handleActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := bearerToken(r)
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, err := p.validator.validate(token)
	if err != nil {
		slog.Warn("teams: rejected activity", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p.dispatch(claims, body)

	w.WriteHeader(http.StatusOK)
}

// dispatch parses an activity, enforces serviceURL binding + authorization +
// the engagement gate, and forwards a core.Message to the engine. Gated-out or
// malformed activities are dropped silently (the webhook still returns 200).
func (p *Platform) dispatch(claims jwt.MapClaims, body []byte) {
	a, err := parseActivity(body)
	if err != nil {
		slog.Warn("teams: bad activity payload", "error", err)
		return
	}
	if !strings.EqualFold(a.Type, "message") {
		return // ignore typing/conversationUpdate/etc.
	}

	// serviceURL binding: a genuine Bot Framework token carries the issuer's
	// serviceurl claim. Rejecting a body whose serviceUrl doesn't match prevents
	// a replayed valid token from redirecting the bot's authenticated replies
	// (and the bearer token they carry) to an attacker-controlled host.
	if !serviceURLClaimMatches(claims, a.ServiceURL) {
		slog.Warn("teams: serviceUrl claim mismatch; dropping activity")
		return
	}

	action := a.cardAction()
	isCardAction := action != ""
	content := a.cleanText()
	if content == "" && !isCardAction {
		return // empty message with no card action
	}
	// Authorize before touching engagement so an unauthorized @mention cannot
	// flip a conversation into the engaged set.
	if !core.AllowList(p.cfg.allowFrom, userID(a)) {
		slog.Debug("teams: message from unauthorized user", "user", userID(a))
		return
	}
	if !p.shouldHandle(a, isCardAction) {
		return
	}

	sessionKey := p.sessionKey(a)
	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "teams",
		MessageID:  a.ID,
		ChannelID:  a.Conversation.ID,
		UserID:     userID(a),
		UserName:   a.From.Name,
		ChatName:   a.Conversation.Name,
		ReplyCtx: replyContext{
			serviceURL:     a.ServiceURL,
			conversationID: a.Conversation.ID,
			sessionKey:     sessionKey,
			activityID:     a.ID,
			botAccount:     a.Recipient,
			userAccount:    a.From,
		},
	}
	if isCardAction {
		msg.Content = action
		msg.IsPermissionResponse = true
	} else {
		msg.Content = content
	}
	p.handler(p, msg)
}

// serviceURLClaimMatches reports whether the token's serviceurl claim matches
// the activity serviceUrl. When the claim is absent (e.g. the Bot Framework
// Emulator), it does not block — a replayed real channel token always carries
// the claim, which is what the binding defends against.
func serviceURLClaimMatches(claims jwt.MapClaims, activityServiceURL string) bool {
	claimed, ok := claims["serviceurl"].(string)
	if !ok || claimed == "" {
		return true
	}
	return strings.TrimRight(claimed, "/") == strings.TrimRight(activityServiceURL, "/")
}

// userID prefers the stable AAD object ID, falling back to the channel-scoped ID.
func userID(a *activity) string {
	if a.From.AADObjectID != "" {
		return a.From.AADObjectID
	}
	return a.From.ID
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}
