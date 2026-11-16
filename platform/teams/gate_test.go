package teams

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func gateClick(value string) []byte {
	v, _ := json.Marshal(map[string]string{"action": value})
	return mustJSON(activity{
		Type:         "message",
		ID:           "click-1",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "user-1", AADObjectID: "aad-1", Name: "Ada"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "conv-A;messageid=root"},
		Value:        v,
	})
}

// gateOrderSender records, for each send, whether the gate command had already
// run, which the stub command shows by creating marker.
type gateOrderSender struct {
	*fakeSender
	marker       string
	afterCommand []bool
}

func (s *gateOrderSender) send(ctx context.Context, rc replyContext, a outboundActivity) (string, error) {
	_, err := os.Stat(s.marker)
	s.afterCommand = append(s.afterCommand, err == nil)
	return s.fakeSender.send(ctx, rc, a)
}

func TestGateCard_ClickResolvesOnceAndNeverReachesEngine(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "gate.sh")
	stub := "#!/bin/sh\ncat >/dev/null\ntouch '" + marker + "'\necho '{\"outcome\":\"published\",\"message\":\"POC: would publish r1\"}'\n"
	if err := os.WriteFile(script, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSender{id: "card-1"}
	conn := &gateOrderSender{fakeSender: fs, marker: marker}
	p := teamsPlatform("thread")
	p.conn = conn
	p.cfg.gateCommand = script
	p.cfg.cardLoadingText = "Working on it…"
	h, got := collector()
	p.handler = h

	var reports []string
	var placeholders []core.StreamingCard
	rc := replyContext{serviceURL: "https://smba.example/", conversationID: "conv-A;messageid=root"}
	card := core.GateCard{RequestID: "r1", Nonce: "n1", Title: "Publish", Body: "Publish branch?", ConfirmLabel: "Publish"}
	onOutcome := func(r string, placeholder core.StreamingCard) {
		reports = append(reports, r)
		placeholders = append(placeholders, placeholder)
	}
	if err := p.PostGateCard(context.Background(), "teams:conv-A;messageid=root", rc, card, onOutcome); err != nil {
		t.Fatal(err)
	}

	p.dispatch(nil, messageActivity("conv-A;messageid=root", "user-1", "ok", true))
	if len(fs.updates) != 0 || p.gates["r1"] == nil {
		t.Fatalf("typed text must leave the gate card open")
	}

	p.dispatch(nil, gateClick("gate:r1:n1"))
	p.dispatch(nil, gateClick("gate:r1:n1"))

	if len(*got) != 1 {
		t.Fatalf("only the typed message may reach the engine, got %d messages", len(*got))
	}
	if len(fs.updates) != 1 || fs.updatedIDs[0] != "card-1" {
		t.Fatalf("card must be edited exactly once, updates=%v", fs.updatedIDs)
	}
	if status := gateCardStatus(t, fs.updates[0]); status != "**Publish** — by Ada" {
		t.Errorf("card status = %q", status)
	}
	if _, ok := fs.updates[0].Attachments[0].Content.(map[string]any)["actions"]; ok {
		t.Errorf("clicked card must have no buttons")
	}

	if len(conn.afterCommand) != 2 || conn.afterCommand[1] {
		t.Fatalf("placeholder must be posted before the gate command runs, sends after command = %v", conn.afterCommand)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gate command did not run: %v", err)
	}
	body := fs.last.Attachments[0].Content.(map[string]any)["body"].([]map[string]any)
	if len(body) != 1 || body[0]["text"] != "Working on it…" {
		t.Errorf("last send must be the loading placeholder, body = %v", body)
	}

	if len(reports) != 1 || !strings.Contains(reports[0], `"published"`) {
		t.Errorf("reports = %v", reports)
	}
	if len(placeholders) != 1 || placeholders[0] == nil {
		t.Errorf("the outcome must carry the posted placeholder, got %v", placeholders)
	}
}

// gateCardStatus returns the status line of an edited gate card.
func gateCardStatus(t *testing.T, a outboundActivity) string {
	t.Helper()
	body := a.Attachments[0].Content.(map[string]any)["body"].([]map[string]any)
	if len(body) != 3 {
		t.Fatalf("edited card has no status line, body = %v", body)
	}
	return body[2]["text"].(string)
}
