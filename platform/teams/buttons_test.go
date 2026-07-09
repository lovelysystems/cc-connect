package teams

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

var permButtons = [][]core.ButtonOption{
	{{Text: "Allow", Data: "perm:allow"}, {Text: "Deny", Data: "perm:deny"}},
	{{Text: "Allow all", Data: "perm:allow_all"}},
}

// cardActions pulls the Action.Submit action strings out of a card attachment.
func cardActions(t *testing.T, a outboundActivity) []string {
	t.Helper()
	if len(a.Attachments) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(a.Attachments))
	}
	card, ok := a.Attachments[0].Content.(map[string]any)
	if !ok {
		t.Fatalf("attachment content is not a card: %T", a.Attachments[0].Content)
	}
	actions, ok := card["actions"].([]map[string]any)
	if !ok {
		return nil
	}
	var out []string
	for _, act := range actions {
		data, _ := act["data"].(map[string]any)
		if s, _ := data["action"].(string); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func TestSendWithButtons_FoldsIntoActiveCard(t *testing.T) {
	fs := &fakeSender{id: "m1"}
	p := &Platform{conn: fs, cfg: config{}}
	if _, err := p.CreateStreamingCard(context.Background(), cardCtx()); err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}

	if err := p.SendWithButtons(context.Background(), cardCtx(), "needs permission", permButtons); err != nil {
		t.Fatalf("SendWithButtons: %v", err)
	}
	// Folded via the card's in-place edit (update), not a fresh send/replyTo.
	if len(fs.updates) == 0 {
		t.Fatalf("prompt should fold into the active card via update, got updates=%d replied=%d", len(fs.updates), len(fs.replied))
	}
	got := cardActions(t, fs.updates[len(fs.updates)-1])
	if len(got) != 3 || got[0] != "perm:allow" || got[2] != "perm:allow_all" {
		t.Errorf("folded card actions = %v", got)
	}
}

func TestSendWithButtons_NoActiveCardSendsStandalone(t *testing.T) {
	fs := &fakeSender{id: "m1"}
	p := &Platform{conn: fs, cfg: config{}}
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1", activityID: "a1"}

	if err := p.SendWithButtons(context.Background(), rc, "needs permission", permButtons); err != nil {
		t.Fatalf("SendWithButtons: %v", err)
	}
	if len(fs.replied) != 1 {
		t.Fatalf("no active card -> standalone threaded card, got replied=%d updates=%d", len(fs.replied), len(fs.updates))
	}
	got := cardActions(t, fs.replied[0])
	if len(got) != 3 {
		t.Errorf("standalone card actions = %v", got)
	}
}

func TestSendWithButtons_UnthreadedUsesSend(t *testing.T) {
	fs := &fakeSender{id: "m1"}
	p := &Platform{conn: fs, cfg: config{}}
	rc := replyContext{serviceURL: "https://s/", conversationID: "c1"} // no activityID, no card

	if err := p.SendWithButtons(context.Background(), rc, "q", [][]core.ButtonOption{{{Text: "A", Data: "askq:0:1"}}}); err != nil {
		t.Fatalf("SendWithButtons: %v", err)
	}
	if len(fs.replied) != 0 || len(fs.last.Attachments) != 1 {
		t.Fatalf("unthreaded no-card should use send, got replied=%d last=%+v", len(fs.replied), fs.last)
	}
	if got := cardActions(t, fs.last); len(got) != 1 || got[0] != "askq:0:1" {
		t.Errorf("askq action = %v", got)
	}
}

func TestSendWithButtons_InvalidReplyCtx(t *testing.T) {
	p := &Platform{conn: &fakeSender{}, cfg: config{}}
	if err := p.SendWithButtons(context.Background(), "nope", "x", permButtons); err == nil {
		t.Fatal("want error for invalid reply context")
	}
}
