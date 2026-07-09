package teams

import (
	"context"
	"testing"
)

type fakeSender struct {
	last        outboundActivity
	rc          replyContext
	id          string
	err         error // injected error for send/replyTo/update
	attachErr   error // if set, returned by send/replyTo only for an activity carrying a media attachment (ContentURL); lets a test fail an image send while a text notice succeeds
	updates     []outboundActivity
	updatedIDs  []string
	replied     []outboundActivity
	repliedToID []string

	// fetch behavior: keyed by requested URL, falling back to fetchDefault.
	fetchByURL     map[string]fetchResult
	fetchDefault   fetchResult
	fetchedURLs    []string
	fetchwithToken []bool
}

type fetchResult struct {
	data    []byte
	outcome fetchOutcome
}

func (f *fakeSender) fetch(_ context.Context, url string, withToken bool, _ int64) ([]byte, fetchOutcome) {
	f.fetchedURLs = append(f.fetchedURLs, url)
	f.fetchwithToken = append(f.fetchwithToken, withToken)
	if r, ok := f.fetchByURL[url]; ok {
		return r.data, r.outcome
	}
	return f.fetchDefault.data, f.fetchDefault.outcome
}

// errFor returns attachErr for a media-bearing activity (an attachment with a
// ContentURL), else the generic injected err. Lets a test fail an image send
// with a specific error while a following text reply (the notice) succeeds.
func (f *fakeSender) errFor(a outboundActivity) error {
	if f.attachErr != nil {
		for _, att := range a.Attachments {
			if att.ContentURL != "" {
				return f.attachErr
			}
		}
	}
	return f.err
}

func (f *fakeSender) send(_ context.Context, rc replyContext, a outboundActivity) (string, error) {
	f.last = a
	f.rc = rc
	return f.id, f.errFor(a)
}

func (f *fakeSender) replyTo(_ context.Context, _ replyContext, activityID string, a outboundActivity) error {
	f.replied = append(f.replied, a)
	f.repliedToID = append(f.repliedToID, activityID)
	return f.errFor(a)
}

func (f *fakeSender) update(_ context.Context, _ replyContext, activityID string, a outboundActivity) error {
	f.updates = append(f.updates, a)
	f.updatedIDs = append(f.updatedIDs, activityID)
	return f.err
}

func TestReply_ThreadsViaReplyEndpoint(t *testing.T) {
	fs := &fakeSender{}
	p := &Platform{conn: fs}
	rc := replyContext{
		serviceURL: "https://s/", conversationID: "c1", activityID: "a1",
		botAccount: channelAccount{ID: "bot"}, userAccount: channelAccount{ID: "user"},
	}

	if err := p.Reply(context.Background(), rc, "hi there"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if len(fs.replied) != 1 || fs.repliedToID[0] != "a1" {
		t.Fatalf("Reply should use the reply endpoint for activity a1, got %+v / %v", fs.replied, fs.repliedToID)
	}
	got := fs.replied[0]
	if got.Type != "message" || got.Text != "hi there" {
		t.Errorf("activity = %+v", got)
	}
	// Conversation-reference envelope present: from=bot, recipient=user.
	if got.From == nil || got.From.ID != "bot" || got.Recipient == nil || got.Recipient.ID != "user" {
		t.Errorf("missing/incorrect envelope: from=%+v recipient=%+v", got.From, got.Recipient)
	}
	if got.Conversation == nil || got.Conversation.ID != "c1" {
		t.Errorf("missing conversation envelope: %+v", got.Conversation)
	}
}

func TestReply_FallsBackToSendWithoutActivityID(t *testing.T) {
	fs := &fakeSender{}
	p := &Platform{conn: fs}
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1"} // no activityID

	if err := p.Reply(context.Background(), rc, "hi"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if len(fs.replied) != 0 || fs.last.Text != "hi" {
		t.Errorf("expected fallback to send, got replied=%v last=%+v", fs.replied, fs.last)
	}
}

func TestSend_PostsNewMessage(t *testing.T) {
	fs := &fakeSender{}
	p := &Platform{conn: fs}
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1"}

	if err := p.Send(context.Background(), rc, "broadcast"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fs.last.Type != "message" || fs.last.Text != "broadcast" {
		t.Errorf("activity = %+v", fs.last)
	}
	if len(fs.replied) != 0 {
		t.Errorf("Send must not use the reply endpoint")
	}
}

func TestReply_RejectsBadReplyCtx(t *testing.T) {
	p := &Platform{conn: &fakeSender{}}
	if err := p.Reply(context.Background(), "not-a-ctx", "x"); err == nil {
		t.Fatal("expected error for invalid reply context")
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p := &Platform{}
	got, err := p.ReconstructReplyCtx("teams:conv-42")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx: %v", err)
	}
	rc, ok := got.(replyContext)
	if !ok || rc.conversationID != "conv-42" {
		t.Fatalf("reconstructed = %+v", got)
	}

	if _, err := p.ReconstructReplyCtx("slack:foo"); err == nil {
		t.Error("expected error for non-teams session key")
	}
}
