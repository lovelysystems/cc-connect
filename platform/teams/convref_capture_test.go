package teams

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/golang-jwt/jwt/v5"
)

func captureTestPlatform(scope string) *Platform {
	return &Platform{
		cfg:         config{appID: "bot-1", sessionScope: scope},
		engaged:     newEngagement(""),
		convRefs:    newConvRefStore(""),
		dispatchSem: make(chan struct{}, maxConcurrentDispatch),
		handler:     func(core.Platform, *core.Message) {},
	}
}

func convrefPersonalActivity(conv, serviceURL string) []byte {
	return mustJSON(activity{
		Type:         "message",
		ID:           "act-1",
		Text:         "hi",
		ServiceURL:   serviceURL,
		From:         channelAccount{ID: "user-1", Name: "User"},
		Recipient:    channelAccount{ID: "bot-1", Name: "bot"},
		Conversation: conversationAccount{ID: conv, ConversationType: "personal"},
	})
}

func TestCapture_PersonalUpsertsReference(t *testing.T) {
	p := captureTestPlatform("thread")
	p.dispatch(jwt.MapClaims{}, convrefPersonalActivity("19:1on1", "https://smba.example/"))

	ref, ok := p.convRefs.lookup("19:1on1")
	if !ok {
		t.Fatal("expected a captured reference for the personal conversation")
	}
	if ref.ServiceURL != "https://smba.example/" || ref.ConversationID != "19:1on1" {
		t.Errorf("captured ref = %+v", ref)
	}
	if ref.BotAccount.ID != "bot-1" {
		t.Errorf("missing bot account in captured ref: %+v", ref.BotAccount)
	}
}

// TestCapture_ChannelScopeKeyMatchesReconstruct proves the capture and
// reconstruct keys agree under channel scope, where sessionKey() strips the
// ";messageid=" suffix. Keying the store on the raw conversation id (as first
// drafted) would miss here and reproduce the delivery failure for channel scope.
func TestCapture_ChannelScopeKeyMatchesReconstruct(t *testing.T) {
	p := captureTestPlatform("channel")
	conv := "19:chan@thread.tacv2;messageid=root1"
	p.dispatch(jwt.MapClaims{}, messageActivity(conv, "user-1", "hello", true))

	// The engine reconstructs from the same session key sessionKey() produced.
	got, err := p.ReconstructReplyCtx("teams:19:chan@thread.tacv2")
	if err != nil {
		t.Fatalf("channel-scope reconstruct should hit the store: %v", err)
	}
	rc := got.(replyContext)
	if rc.serviceURL == "" {
		t.Fatal("channel-scope proactive send not addressable (empty serviceURL)")
	}
	// The stored conversationID is the full activity id (the thread to POST to),
	// even though the lookup key is the stripped channel root.
	if rc.conversationID != conv {
		t.Errorf("conversationID = %q, want full id %q", rc.conversationID, conv)
	}
}

func TestCapture_UnauthorizedNotCaptured(t *testing.T) {
	p := captureTestPlatform("thread")
	p.cfg.allowFrom = "someone-else" // user-1 is not on the allow list
	p.dispatch(jwt.MapClaims{}, convrefPersonalActivity("19:1on1", "https://smba.example/"))

	if _, ok := p.convRefs.lookup("19:1on1"); ok {
		t.Error("unauthorized traffic must not be captured into the store")
	}
}

// TestProactiveSend_AddressableAfterInbound is the regression test for the
// reported bug: a proactive send (cron/timer/heartbeat) against a conversation
// seen only via a prior inbound activity must be addressable — the original
// failure was an empty serviceURL rejected by connector.send.
func TestProactiveSend_AddressableAfterInbound(t *testing.T) {
	p := captureTestPlatform("thread")
	fs := &fakeSender{}
	p.conn = fs

	// The bot sees the conversation once, inbound.
	p.dispatch(jwt.MapClaims{}, convrefPersonalActivity("19:1on1", "https://smba.example/"))

	// Later, with no inbound activity in hand, a timer reconstructs and sends.
	rc, err := p.ReconstructReplyCtx("teams:19:1on1")
	if err != nil {
		t.Fatalf("reconstruct after inbound: %v", err)
	}
	if err := p.Send(context.Background(), rc, "reminder"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// The reply context reaching the connector must carry both fields
	// connector.send requires; an empty serviceURL was the reported failure.
	if fs.rc.serviceURL == "" || fs.rc.conversationID == "" {
		t.Fatalf("proactive send not addressable: %+v", fs.rc)
	}
	if fs.last.Text != "reminder" {
		t.Errorf("sent activity = %+v", fs.last)
	}
}

func TestProactiveSend_ColdConversationErrorsNoSend(t *testing.T) {
	p := captureTestPlatform("thread")
	fs := &fakeSender{}
	p.conn = fs

	if _, err := p.ReconstructReplyCtx("teams:never-seen"); err == nil {
		t.Fatal("a conversation the bot never saw should return a clear error")
	}
	if fs.last.Text != "" {
		t.Error("no proactive send should happen for a cold conversation")
	}
}
