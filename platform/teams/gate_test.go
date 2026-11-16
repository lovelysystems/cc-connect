package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	card := core.GateCard{RequestID: "r1", Nonce: "n1", Title: "Publish", Body: "Publish branch?", ConfirmLabel: "Publish"}
	onClick := func() func(string, core.StreamingCard) {
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

func TestGateCard_PromptButtonsAreActionExecute(t *testing.T) {
	c := gatePromptCard(core.GateCard{RequestID: "r1", Nonce: "n1", Title: "Publish", ConfirmLabel: "Publish"})
	if c["version"] != "1.4" {
		t.Errorf("version = %v, want 1.4", c["version"])
	}
	actions := c["actions"].([]map[string]any)
	want := []string{"gate:r1:n1", "gate:r1:n1:cancel"}
	if len(actions) != len(want) {
		t.Fatalf("actions = %v", actions)
	}
	for i, a := range actions {
		if a["type"] != "Action.Execute" || a["verb"] != want[i] || a["data"].(map[string]any)["action"] != want[i] {
			t.Errorf("action %d = %v, want Action.Execute carrying %q", i, a, want[i])
		}
	}
}

func gateInvoke(t *testing.T, value string) []byte {
	t.Helper()
	v, err := json.Marshal(map[string]any{
		"action":  map[string]any{"type": "Action.Execute", "verb": value, "data": map[string]any{"action": value}},
		"trigger": "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	return mustJSON(activity{
		Type:         "invoke",
		Name:         "adaptiveCard/action",
		ID:           "invoke-1",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "user-1", AADObjectID: "aad-1", Name: "Ada"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: "conv-A;messageid=root"},
		Value:        v,
	})
}

// markerCheckingRecorder records whether marker existed when the response body
// was written.
type markerCheckingRecorder struct {
	*httptest.ResponseRecorder
	marker        string
	markerAtWrite []bool
}

func (r *markerCheckingRecorder) Write(b []byte) (int, error) {
	_, err := os.Stat(r.marker)
	r.markerAtWrite = append(r.markerAtWrite, err == nil)
	return r.ResponseRecorder.Write(b)
}

func postInvoke(t *testing.T, p *Platform, s *testSigner, body []byte, marker string) *markerCheckingRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.sign(t, baseClaims()))
	rec := &markerCheckingRecorder{ResponseRecorder: httptest.NewRecorder(), marker: marker}
	p.handleActivity(rec, req)
	return rec
}

func TestGateCard_InvokeIsAnsweredWithTheClickedCardBeforeTheCommandRuns(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "gate.sh")
	stub := "#!/bin/sh\ncat >/dev/null\ntouch '" + marker + "'\necho '{\"outcome\":\"published\",\"message\":\"POC: would publish r1\"}'\n"
	if err := os.WriteFile(script, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newTestSigner(t)
	p := testPlatform(s)
	p.engaged = newEngagement("")
	fs := &fakeSender{id: "card-1"}
	p.conn = fs
	p.cfg.gateCommand = script
	p.cfg.cardLoadingText = "Working on it…"
	h, got := collector()
	p.handler = h

	rc := replyContext{serviceURL: "https://smba.example/", conversationID: "conv-A;messageid=root"}
	card := core.GateCard{RequestID: "r1", Nonce: "n1", Title: "Publish", Body: "Publish branch?", ConfirmLabel: "Publish"}
	clicks := 0
	reports := make(chan string, 1)
	onClick := func() func(string, core.StreamingCard) {
		clicks++
		return func(r string, _ core.StreamingCard) { reports <- r }
	}
	if err := p.PostGateCard(context.Background(), "teams:conv-A;messageid=root", rc, card, onClick); err != nil {
		t.Fatal(err)
	}

	rec := postInvoke(t, p, s, gateInvoke(t, "gate:r1:n1"), marker)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(rec.markerAtWrite) == 0 || rec.markerAtWrite[0] {
		t.Fatalf("the invoke must be answered before the gate command runs, marker at write = %v", rec.markerAtWrite)
	}
	if clicks != 1 {
		t.Errorf("the click must reach the engine before the answer, clicks = %d", clicks)
	}
	var resp struct {
		StatusCode int            `json:"statusCode"`
		Type       string         `json:"type"`
		Value      map[string]any `json:"value"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invoke response: %v", err)
	}
	if resp.StatusCode != 200 || resp.Type != adaptiveCardContentType {
		t.Fatalf("invoke response = %+v", resp)
	}
	body := resp.Value["body"].([]any)
	if len(body) != 3 || body[2].(map[string]any)["text"] != "**Publish** — by Ada" {
		t.Errorf("answered card body = %v", body)
	}
	if _, ok := resp.Value["actions"]; ok {
		t.Errorf("answered card must have no buttons")
	}

	select {
	case r := <-reports:
		if !strings.Contains(r, `"published"`) {
			t.Errorf("report = %q", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("outcome never reported")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gate command did not run: %v", err)
	}
	if len(fs.updates) != 0 {
		t.Errorf("an invoke click must not edit the card through the connector, updates = %d", len(fs.updates))
	}
	if len(*got) != 0 {
		t.Errorf("a gate invoke must never reach the engine as a message")
	}

	again := postInvoke(t, p, s, gateInvoke(t, "gate:r1:n1"), marker)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), "application/vnd.microsoft.activity.message") {
		t.Errorf("a resolved gate invoke must get a 200 message response, got %d %s", again.Code, again.Body.String())
	}
	if clicks != 1 {
		t.Errorf("a resolved gate must not be clicked again, clicks = %d", clicks)
	}
}

func TestGateCard_InvokeWithMismatchedServiceURLIsIgnored(t *testing.T) {
	s := newTestSigner(t)
	p := testPlatform(s)
	p.conn = &fakeSender{id: "card-1"}
	rc := replyContext{serviceURL: "https://smba.example/", conversationID: "conv-A;messageid=root"}
	clicks := 0
	onClick := func() func(string, core.StreamingCard) {
		clicks++
		return func(string, core.StreamingCard) {}
	}
	if err := p.PostGateCard(context.Background(), "teams:conv-A;messageid=root", rc, core.GateCard{RequestID: "r1", Nonce: "n1"}, onClick); err != nil {
		t.Fatal(err)
	}

	claims := baseClaims()
	claims["serviceurl"] = "https://elsewhere.example/"
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(gateInvoke(t, "gate:r1:n1")))
	req.Header.Set("Authorization", "Bearer "+s.sign(t, claims))
	rec := httptest.NewRecorder()
	p.handleActivity(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "application/vnd.microsoft.activity.message") {
		t.Errorf("got %d %s, want a 200 message response", rec.Code, rec.Body.String())
	}
	if clicks != 0 || p.gates["r1"] == nil {
		t.Errorf("a gate invoke from a mismatched serviceUrl must leave the gate open")
	}
}
