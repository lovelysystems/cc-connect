// Package teams implements a native cc-connect platform for Microsoft Teams.
//
// Teams differs from the outbound-connection platforms (Slack socket mode,
// Feishu/WeCom websockets): the Bot Framework delivers activities by POSTing to
// a public HTTPS webhook, and there is no Microsoft Go SDK. This package
// terminates the Bot Framework itself — inbound JWT validation, the outbound Bot
// Connector REST client with AAD auth, and the Teams streaming-message protocol.
package teams

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterPlatform("teams", New)
}

// maxConcurrentDispatch caps in-flight agent turns spawned from the webhook. The
// handler acks fast and processes on a goroutine (Bot Framework retry avoidance),
// so without a cap a flood of authenticated activities would spawn unbounded
// concurrent turns. At capacity the webhook sheds with 503 and the Bot Connector
// retries — mirroring the M365 Agents SDK's bounded background queue.
const maxConcurrentDispatch = 16

// Platform is the cc-connect Teams connector.
type Platform struct {
	cfg     config
	handler core.MessageHandler

	validator   *inboundValidator
	engaged     *engagement
	conn        sender
	server      *http.Server
	dispatchSem chan struct{} // bounds concurrent async dispatch goroutines
}

// sender abstracts the Bot Connector calls for testability.
type sender interface {
	send(ctx context.Context, rc replyContext, a outboundActivity) (string, error)
	replyTo(ctx context.Context, rc replyContext, activityID string, a outboundActivity) error
	update(ctx context.Context, rc replyContext, activityID string, a outboundActivity) error
	fetch(ctx context.Context, url string, withToken bool, maxBytes int64) ([]byte, fetchOutcome)
}

// Optional-interface assertions: the engine type-switches on these to drive
// streaming preview and prompt formatting.
var (
	_ core.ReplyContextReconstructor     = (*Platform)(nil)
	_ core.FormattingInstructionProvider = (*Platform)(nil)
	_ core.StreamingCardPlatform         = (*Platform)(nil)
)

// New builds a Teams platform from the config.toml options table.
func New(opts map[string]any) (core.Platform, error) {
	cfg, err := parseConfig(opts)
	if err != nil {
		return nil, err
	}
	core.CheckAllowFrom("teams", cfg.allowFrom)
	return &Platform{
		cfg:         cfg,
		engaged:     newEngagement(engagementPath(cfg.dataDir, cfg.project)),
		dispatchSem: make(chan struct{}, maxConcurrentDispatch),
	}, nil
}

func (p *Platform) Name() string { return "teams" }

// Start brings up the inbound webhook server: it sets up the JWT validator
// (fetching the Bot Framework JWKS) and listens for Bot Connector activity POSTs.
func (p *Platform) Start(handler core.MessageHandler) error {
	p.handler = handler
	if p.validator == nil {
		v, err := newInboundValidator(p.cfg)
		if err != nil {
			return err
		}
		p.validator = v
	}
	if p.conn == nil {
		p.conn = newConnector(newTokenSource(p.cfg))
	}
	mux := http.NewServeMux()
	mux.HandleFunc(p.cfg.webhookPath, p.handleActivity)
	// Timeouts on a public-facing listener guard against slowloris-style clients.
	p.server = &http.Server{
		Addr:              ":" + p.cfg.webhookPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		slog.Info("teams: webhook listening", "port", p.cfg.webhookPort, "path", p.cfg.webhookPath)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("teams: webhook server error", "error", err)
		}
	}()
	return nil
}

// Reply posts a message threaded to the originating activity via the Bot
// Framework reply-to-activity endpoint (threading is keyed by the endpoint, not
// a body field). Falls back to a plain send when there is no activity to thread.
func (p *Platform) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("teams: invalid reply context %T", replyCtx)
	}
	if rc.activityID == "" {
		_, err := p.conn.send(ctx, rc, newMessageActivity(rc, content))
		return err
	}
	return p.conn.replyTo(ctx, rc, rc.activityID, newMessageActivity(rc, content))
}

// Send posts a new message activity into the conversation.
func (p *Platform) Send(ctx context.Context, replyCtx any, content string) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("teams: invalid reply context %T", replyCtx)
	}
	_, err := p.conn.send(ctx, rc, newMessageActivity(rc, content))
	return err
}

// ReconstructReplyCtx rebuilds a reply context from a session key. Only the
// conversation id is recoverable from the key; the per-activity serviceURL is
// not encoded, so proactive sends (cron→Teams) need a conversation-reference
// store — deferred follow-up. Implementing this satisfies the optional
// ReplyContextReconstructor interface used by the engine.
func (p *Platform) ReconstructReplyCtx(sessionKey string) (any, error) {
	conv, err := conversationFromSessionKey(sessionKey)
	if err != nil {
		return nil, err
	}
	return replyContext{conversationID: conv}, nil
}

// Stop shuts down the webhook server.
func (p *Platform) Stop() error {
	if p.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return p.server.Shutdown(ctx)
	}
	return nil
}
