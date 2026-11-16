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

func TestGateCard_ClickResolvesOnceAndNeverReachesEngine(t *testing.T) {
	script := filepath.Join(t.TempDir(), "gate.sh")
	stub := "#!/bin/sh\ncat >/dev/null\necho '{\"outcome\":\"published\",\"message\":\"POC: would publish r1\"}'\n"
	if err := os.WriteFile(script, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSender{id: "card-1"}
	p := teamsPlatform("thread")
	p.conn = fs
	p.cfg.gateCommand = script
	h, got := collector()
	p.handler = h

	var reports []string
	rc := replyContext{serviceURL: "https://smba.example/", conversationID: "conv-A;messageid=root"}
	card := core.GateCard{RequestID: "r1", Nonce: "n1", Title: "Publish", Body: "Publish branch?"}
	if err := p.PostGateCard(context.Background(), "teams:conv-A;messageid=root", rc, card, func(r string) { reports = append(reports, r) }); err != nil {
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
	if len(fs.updates) != 2 || fs.updatedIDs[0] != "card-1" || fs.updatedIDs[1] != "card-1" {
		t.Fatalf("card must be edited twice, updates=%v", fs.updatedIDs)
	}
	if len(fs.last.Attachments) != 1 || fs.last.Text != "" {
		t.Errorf("no message may follow the card, last send = %+v", fs.last)
	}
	if status := gateCardStatus(t, fs.updates[0]); status != "Publishing…" {
		t.Errorf("first card status = %q", status)
	}
	if _, ok := fs.updates[0].Attachments[0].Content.(map[string]any)["actions"]; ok {
		t.Errorf("publishing card must have no buttons")
	}
	if status := gateCardStatus(t, fs.updates[1]); status != "Published by Ada." {
		t.Errorf("final card status = %q", status)
	}
	if len(reports) != 1 || !strings.Contains(reports[0], `"published"`) {
		t.Errorf("reports = %v", reports)
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
