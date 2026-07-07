package teams

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	"github.com/golang-jwt/jwt/v5"
)

// maxBodyBytes caps the activity payload read from the connector.
const maxBodyBytes = 1 << 20 // 1 MiB

// handleActivity is the Bot Connector webhook entry point. It authenticates the
// request and reads the body synchronously, then acks 202 and runs the agent turn
// on a background goroutine. Bot Framework expects a fast ack (~15s) and retries
// on timeout; a slow turn (e.g. a cold agent start) under a synchronous ack would
// trigger a retry and a duplicate dispatch. Acking before dispatch matches the
// M365 Agents SDK, which queues activities to a background worker and returns 202.
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

	// Ack first, process the turn asynchronously (the card-only connector never
	// returns Invoke/ExpectReplies responses, so nothing needs a synchronous body).
	// A bounded semaphore caps concurrent turns; at capacity we shed with 503 so
	// the Bot Connector retries rather than letting turns spawn unbounded.
	select {
	case p.dispatchSem <- struct{}{}:
		go func() {
			defer func() { <-p.dispatchSem }()
			p.dispatch(claims, body)
		}()
		w.WriteHeader(http.StatusAccepted)
	default:
		slog.Warn("teams: dispatch pool saturated; shedding activity")
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}
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
	// Optional host allowlist: reject a serviceURL outside the configured hosts
	// before any outbound POST carries the bot's bearer token to it. Off by default.
	if !serviceURLAllowed(a.ServiceURL, p.cfg.serviceURLAllowlist) {
		slog.Warn("teams: serviceUrl host not in allowlist; dropping activity", "service_url", a.ServiceURL)
		return
	}

	action := a.cardAction()
	isCardAction := action != ""
	content := a.cleanText()
	// Inbound media is 1:1 only; a channel/group attachment is ignored (R5). A
	// message carrying attachments still dispatches even with empty text (R3).
	hasMedia := a.isPersonal() && len(a.Attachments) > 0
	if content == "" && !isCardAction && !hasMedia {
		return // empty message with no card action and no attachment
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
	rc := replyContext{
		serviceURL:     a.ServiceURL,
		conversationID: a.Conversation.ID,
		activityID:     a.ID,
		botAccount:     a.Recipient,
		userAccount:    a.From,
	}
	msg := &core.Message{
		SessionKey: sessionKey,
		Platform:   "teams",
		MessageID:  a.ID,
		ChannelID:  a.Conversation.ID,
		UserID:     userID(a),
		UserName:   a.From.Name,
		ChatName:   a.Conversation.Name,
		ReplyCtx:   rc,
	}
	if isCardAction {
		msg.Content = action
		msg.IsPermissionResponse = true
	} else {
		msg.Content = content
		if hasMedia {
			images, files, failed := p.downloadInboundMedia(a)
			msg.Images = images
			msg.Files = files
			if failed > 0 {
				slog.Warn("teams: some inbound attachments were skipped", "count", failed)
			}
		}
	}
	p.handler(p, msg)
}

// downloadInboundMedia downloads a 1:1 activity's file and image attachments,
// returning them as core attachments plus a count of downloads that were too
// large or failed (surfaced to the user by the caller). It runs on the async
// dispatch goroutine, so the blocking HTTP here never delays the webhook ack.
func (p *Platform) downloadInboundMedia(a *activity) (images []core.ImageAttachment, files []core.FileAttachment, failed int) {
	if p.conn == nil {
		return nil, nil, 0
	}
	ctx := context.Background()
	max := p.cfg.maxAttachmentBytes
	if max <= 0 {
		max = defaultMaxAttachmentBytes
	}
	for _, att := range a.Attachments {
		switch {
		case att.isFileDownload():
			info, ok := att.downloadInfo()
			if !ok || info.DownloadURL == "" {
				continue // malformed file attachment; nothing to fetch
			}
			// downloadUrl is pre-authenticated: fetch WITHOUT the bot token.
			data, outcome := p.conn.fetch(ctx, info.DownloadURL, false, max)
			if outcome != fetchOK {
				failed++
				continue
			}
			files = append(files, core.FileAttachment{
				MimeType: mimeForFile(att.Name, info.FileType),
				Data:     data,
				FileName: att.Name,
			})
		case att.isImage():
			data, outcome := p.fetchImage(ctx, att, a.ServiceURL, max)
			if outcome != fetchOK {
				failed++
				continue
			}
			images = append(images, core.ImageAttachment{
				MimeType: att.ContentType,
				Data:     data,
				FileName: att.Name,
			})
		}
	}
	return images, files, failed
}

// fetchImage retrieves an inline image attachment. A data: URI is decoded in
// place; otherwise the contentUrl is fetched, attaching the bot bearer token
// only when the URL is on the same host as the JWT-validated serviceURL (the Bot
// Connector attachment endpoint). This keeps the token from ever reaching a
// foreign host embedded in a forged contentUrl.
func (p *Platform) fetchImage(ctx context.Context, att inboundAttachment, serviceURL string, maxBytes int64) ([]byte, fetchOutcome) {
	if strings.HasPrefix(att.ContentURL, "data:") {
		if data, ok := decodeDataURI(att.ContentURL, maxBytes); ok {
			return data, fetchOK
		}
		return nil, fetchFailed
	}
	if att.ContentURL == "" {
		return nil, fetchFailed
	}
	return p.conn.fetch(ctx, att.ContentURL, sameHost(att.ContentURL, serviceURL), maxBytes)
}

// decodeDataURI decodes a base64 data: URI, enforcing the same size cap as a
// network download. Only base64 payloads are supported (Teams inline images).
func decodeDataURI(uri string, maxBytes int64) ([]byte, bool) {
	comma := strings.IndexByte(uri, ',')
	if comma < 0 {
		return nil, false
	}
	meta, payload := uri[:comma], uri[comma+1:]
	if !strings.Contains(meta, "base64") {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	if int64(len(data)) > maxBytes {
		return nil, false
	}
	return data, true
}

// sameHost reports whether two URLs share a host (case-insensitive). Used to gate
// whether the bot bearer token may accompany an image download.
func sameHost(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil || ua.Host == "" {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host)
}

// mimeForFile derives a MIME type from a downloaded file's name or the
// FileDownloadInfo fileType extension, falling back to a generic binary type.
func mimeForFile(name, fileType string) string {
	if ext := filepath.Ext(name); ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			return mt
		}
	}
	if fileType != "" {
		if mt := mime.TypeByExtension("." + strings.TrimPrefix(fileType, ".")); mt != "" {
			return mt
		}
	}
	return "application/octet-stream"
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
