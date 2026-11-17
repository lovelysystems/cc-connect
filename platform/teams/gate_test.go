package teams

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func gateSubmit(value string) []byte {
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
	card := core.GateCard{RequestID: "r1", Nonce: "n1", Eyebrow: "Publish to main", Title: "draft-1", Body: "Goes live on the site once you publish.", ConfirmLabel: "Publish"}
	clicks := 0
	onClick := func() func(string, core.StreamingCard) {
		clicks++
		if len(fs.updates) != 0 {
			t.Errorf("the click must hold the session before the card is edited")
		}
		return func(r string, placeholder core.StreamingCard) {
			reports = append(reports, r)
			placeholders = append(placeholders, placeholder)
		}
	}
	if err := p.PostGateCard(context.Background(), "teams:conv-A;messageid=root", rc, card, onClick); err != nil {
		t.Fatal(err)
	}

	p.dispatch(nil, messageActivity("conv-A;messageid=root", "user-1", "ok", true))
	if len(fs.updates) != 0 || p.gates["r1"] == nil {
		t.Fatalf("typed text must leave the gate card open")
	}

	p.dispatch(nil, gateSubmit("gate:r1:n1"))
	p.dispatch(nil, gateSubmit("gate:r1:n1"))

	if len(*got) != 1 {
		t.Fatalf("only the typed message may reach the engine, got %d messages", len(*got))
	}
	if len(fs.updates) != 1 || fs.updatedIDs[0] != "card-1" {
		t.Fatalf("card must be edited exactly once, updates=%v", fs.updatedIDs)
	}
	if clicks != 1 {
		t.Errorf("onClick must run once, ran %d times", clicks)
	}
	checkGateCardStatus(t, fs.updates[0], "✓ Confirmed by Ada")
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

// checkGateCardStatus checks that an edited gate card ends in a small, subtle
// status line under a divider that reads decision, then the click time as
// Teams date and time functions.
func checkGateCardStatus(t *testing.T, a outboundActivity, decision string) {
	t.Helper()
	body := a.Attachments[0].Content.(map[string]any)["body"].([]map[string]any)
	if len(body) != 4 {
		t.Fatalf("edited card must keep eyebrow, title and body and add a status line, body = %v", body)
	}
	status := body[3]
	if status["size"] != "Small" || status["isSubtle"] != true || status["separator"] != true || status["wrap"] != true {
		t.Errorf("status block = %v, want small, subtle, separated and wrapped", status)
	}
	when := `(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)`
	want := regexp.MustCompile(`^` + regexp.QuoteMeta(decision) + ` · \{\{DATE\(` + when + `, COMPACT\)\}\}, \{\{TIME\(` + when + `\)\}\}$`)
	text, _ := status["text"].(string)
	m := want.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("status text = %q, want %q followed by the click date and time", text, decision)
	}
	if m[1] != m[2] {
		t.Errorf("status date and time name different instants: %q", text)
	}
}

func TestGateCard_PromptButtonsAreActionSubmitWithTeamsFeedbackHiddenAndBlankTooltip(t *testing.T) {
	c := gatePromptCard(core.GateCard{RequestID: "r1", Nonce: "n1", Title: "Publish", ConfirmLabel: "Publish"})
	if c["version"] != "1.5" {
		t.Errorf("version = %v, want 1.5", c["version"])
	}
	actions := c["actions"].([]map[string]any)
	want := []struct{ title, value, style string }{
		{"Publish", "gate:r1:n1", "positive"},
		{"Cancel", "gate:r1:n1:cancel", ""},
	}
	if len(actions) != len(want) {
		t.Fatalf("actions = %v", actions)
	}
	for i, a := range actions {
		data := a["data"].(map[string]any)
		if a["type"] != "Action.Submit" || a["title"] != want[i].title || data["action"] != want[i].value {
			t.Errorf("action %d = %v, want Action.Submit %q carrying %q", i, a, want[i].title, want[i].value)
		}
		if style, _ := a["style"].(string); style != want[i].style {
			t.Errorf("action %d style = %q, want %q", i, style, want[i].style)
		}
		raw, _ := json.Marshal(a["msteams"])
		if string(raw) != `{"feedback":{"hide":true}}` {
			t.Errorf("action %d msteams = %s, want feedback hidden", i, raw)
		}
		if _, ok := data["msteams"]; ok {
			t.Errorf("action %d data carries msteams; Teams reads it from the action", i)
		}
		var sent map[string]any
		rawAction, _ := json.Marshal(a)
		_ = json.Unmarshal(rawAction, &sent)
		if tip, ok := sent["tooltip"]; !ok || tip != " " {
			t.Errorf("action %d tooltip = %#v (present %v), want a single space", i, tip, ok)
		}
	}
}

func TestGateCard_EyebrowIsASmallSubtleLineAboveTheTitle(t *testing.T) {
	body := gatePromptCard(core.GateCard{RequestID: "r1", Nonce: "n1", Eyebrow: "Publish to main", Title: "draft-1", Body: "Goes live."})["body"].([]map[string]any)
	if len(body) != 3 {
		t.Fatalf("body = %v, want eyebrow, title and body", body)
	}
	eyebrow, title := body[0], body[1]
	if eyebrow["text"] != "Publish to main" || eyebrow["size"] != "Small" || eyebrow["weight"] != "Bolder" ||
		eyebrow["isSubtle"] != true || eyebrow["wrap"] != true {
		t.Errorf("eyebrow block = %v, want small, bolder, subtle and wrapped", eyebrow)
	}
	if title["text"] != "draft-1" || title["size"] != "Large" || title["weight"] != "Bolder" ||
		title["wrap"] != true || title["spacing"] != "None" {
		t.Errorf("title block = %v, want large, bolder, wrapped, no spacing under the eyebrow", title)
	}
	if body[2]["text"] != "Goes live." || body[2]["wrap"] != true || body[2]["size"] != nil {
		t.Errorf("body block = %v, want default size, wrapped", body[2])
	}
}

func TestGateCard_NoEyebrowLeavesTheTitleFirst(t *testing.T) {
	body := gatePromptCard(core.GateCard{RequestID: "r1", Nonce: "n1", Title: "draft-1", Body: "Goes live."})["body"].([]map[string]any)
	if len(body) != 2 {
		t.Fatalf("body = %v, want title and body only", body)
	}
	title := body[0]
	if title["text"] != "draft-1" || title["size"] != "Large" || title["weight"] != "Bolder" {
		t.Errorf("title block = %v, want large and bolder", title)
	}
	if _, ok := title["spacing"]; ok {
		t.Errorf("title without an eyebrow keeps default spacing, got %v", title["spacing"])
	}
}

func TestGateCard_CancelClickEditsTheCardAndSkipsTheCommand(t *testing.T) {
	fs := &fakeSender{id: "card-1"}
	p := teamsPlatform("thread")
	p.conn = fs
	p.cfg.gateCommand = filepath.Join(t.TempDir(), "never-runs.sh")
	p.handler, _ = collector()

	var reports []string
	rc := replyContext{serviceURL: "https://smba.example/", conversationID: "conv-A;messageid=root"}
	card := core.GateCard{RequestID: "r1", Nonce: "n1", Eyebrow: "Publish to main", Title: "draft-1", Body: "Goes live on the site once you publish.", ConfirmLabel: "Publish"}
	onClick := func() func(string, core.StreamingCard) {
		return func(r string, _ core.StreamingCard) { reports = append(reports, r) }
	}
	if err := p.PostGateCard(context.Background(), "teams:conv-A;messageid=root", rc, card, onClick); err != nil {
		t.Fatal(err)
	}

	p.dispatch(nil, gateSubmit("gate:r1:n1:cancel"))

	if len(fs.updates) != 1 {
		t.Fatalf("card must be edited exactly once, updates = %d", len(fs.updates))
	}
	checkGateCardStatus(t, fs.updates[0], "✕ Cancelled by Ada")
	if len(reports) != 1 || !strings.Contains(reports[0], `"cancelled"`) {
		t.Errorf("reports = %v", reports)
	}
}
