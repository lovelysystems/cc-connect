package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/golang-jwt/jwt/v5"
)

func signoffTestCard(lines ...core.CardText) *core.Card {
	card := &core.Card{Header: &core.CardHeader{Title: "5 changes"}}
	for _, l := range lines {
		card.Elements = append(card.Elements, l)
	}
	card.Elements = append(card.Elements, core.CardActions{Buttons: []core.CardButton{
		core.PrimaryBtn("Approve", "signoff:r1:n1"),
		core.DefaultBtn("Cancel", "signoff:r1:n1:cancel"),
	}})
	return card
}

func signoffSampleLines() []core.CardText {
	return []core.CardText{
		{Text: "ENTRIES", Style: core.CardTextHeading},
		{Text: "winter-concert-2027", Style: core.CardTextName},
		{Text: "www.example.com · www.example.org", Style: core.CardTextDetail},
		{Text: "🔗 https://tickets.example.com/winter", Style: core.CardTextLink},
	}
}

// signoffRuns returns the TextRuns of the card body's RichTextBlocks, in order.
func signoffRuns(t *testing.T, card map[string]any) []map[string]any {
	t.Helper()
	var runs []map[string]any
	for _, el := range card["body"].([]map[string]any) {
		if el["type"] == "TextBlock" {
			t.Fatalf("request text drawn in a TextBlock: %v", el)
		}
		if el["type"] != "RichTextBlock" {
			continue
		}
		runs = append(runs, el["inlines"].([]map[string]any)...)
	}
	return runs
}

func TestSignoffCardDrawsEveryTextAsATextRun(t *testing.T) {
	card := signoffAdaptiveCard(signoffTestCard(signoffSampleLines()...), nil)
	runs := signoffRuns(t, card)
	if len(runs) != 5 {
		t.Fatalf("runs = %d, want title and four lines", len(runs))
	}
	want := []map[string]any{
		{"text": "5 changes", "size": "Large", "weight": "Bolder"},
		{"text": "ENTRIES", "size": "Small", "weight": "Bolder", "isSubtle": true},
		{"text": "winter-concert-2027", "weight": "Bolder"},
		{"text": "www.example.com · www.example.org", "size": "Small", "isSubtle": true},
		{"text": "🔗 https://tickets.example.com/winter", "size": "Small", "color": "Accent"},
	}
	for i, w := range want {
		if runs[i]["type"] != "TextRun" {
			t.Fatalf("run %d type = %v", i, runs[i]["type"])
		}
		for k, v := range w {
			if runs[i][k] != v {
				t.Fatalf("run %d %s = %v, want %v", i, k, runs[i][k], v)
			}
		}
	}
	if body := card["body"].([]map[string]any); body[1]["separator"] != true {
		t.Fatalf("heading block = %v, want a separator above it", body[1])
	}

	if card["version"] != "1.5" {
		t.Fatalf("version = %v", card["version"])
	}
	actions, _ := card["actions"].([]map[string]any)
	if len(actions) != 2 {
		t.Fatalf("actions = %v, want two buttons", card["actions"])
	}
	for i, a := range actions {
		hide := a["msteams"].(map[string]any)["feedback"].(map[string]any)["hide"]
		if _, ok := a["data"]; ok || a["type"] != "Action.Execute" || hide != true || a["tooltip"] != "" {
			t.Fatalf("action %d = %v", i, a)
		}
	}
	if actions[0]["title"] != "Approve" || actions[0]["style"] != "positive" || actions[0]["verb"] != "signoff:r1:n1" {
		t.Fatalf("confirm = %v", actions[0])
	}
	if actions[1]["title"] != "Cancel" || actions[1]["style"] != nil || actions[1]["verb"] != "signoff:r1:n1:cancel" {
		t.Fatalf("cancel = %v", actions[1])
	}
}

func TestSignoffCardKeepsMarkupAsWritten(t *testing.T) {
	texts := []string{"**x**", "<b>x</b>", `{"type":"AdaptiveCard"}`, "{a} }}", "{ {DATE(x)}}"}
	var lines []core.CardText
	for _, text := range texts {
		lines = append(lines, core.CardText{Text: text, Style: core.CardTextName})
	}
	runs := signoffRuns(t, signoffAdaptiveCard(signoffTestCard(lines...), nil))
	for i, text := range texts {
		if runs[i+1]["text"] != text {
			t.Fatalf("run %d text = %q, want %q", i+1, runs[i+1]["text"], text)
		}
	}
}

func TestSignoffCardKeepsDoubleBracesLiteral(t *testing.T) {
	const wj = "\u2060"
	card := &core.Card{
		Header: &core.CardHeader{Title: "{{DATE(2026-11-17T10:42:00Z)}}"},
		Elements: []core.CardElement{
			core.CardText{Text: "a {{TIME(2026-11-17T10:42:00Z)}} b", Style: core.CardTextName},
			core.CardText{Text: "{{{DATE(x)}}", Style: core.CardTextDetail},
			core.CardActions{Buttons: []core.CardButton{
				core.PrimaryBtn("{{DATE(x)}}", "signoff:r1:n1"),
				core.DefaultBtn("Cancel", "signoff:r1:n1:cancel"),
			}},
		},
	}
	drawn := signoffAdaptiveCard(card, nil)
	runs := signoffRuns(t, drawn)
	want := []string{
		"{" + wj + "{DATE(2026-11-17T10:42:00Z)}}",
		"a {" + wj + "{TIME(2026-11-17T10:42:00Z)}} b",
		"{" + wj + "{" + wj + "{DATE(x)}}",
	}
	for i, w := range want {
		if runs[i]["text"] != w {
			t.Fatalf("run %d text = %q, want %q", i, runs[i]["text"], w)
		}
	}
	actions := drawn["actions"].([]map[string]any)
	if actions[0]["title"] != "{"+wj+"{DATE(x)}}" || actions[1]["title"] != "Cancel" {
		t.Fatalf("button titles = %q, %q", actions[0]["title"], actions[1]["title"])
	}
	if card.Header.Title != "{{DATE(2026-11-17T10:42:00Z)}}" || card.Elements[0].(core.CardText).Text != "a {{TIME(2026-11-17T10:42:00Z)}} b" {
		t.Fatalf("drawing changed the card: %+v", card)
	}

	at := time.Date(2026, 11, 17, 9, 42, 0, 0, time.UTC)
	redrawn := signoffAdaptiveCard(card, &core.SignoffMark{Kind: core.SignoffMarkApproved, By: "Jane Editor", At: at})
	_, line := signoffFooterTexts(t, redrawn)
	if line != "Jane Editor · {{DATE(2026-11-17T09:42:00Z, COMPACT)}}, {{TIME(2026-11-17T09:42:00Z)}}" {
		t.Fatalf("footer = %q", line)
	}
}

func signoffFooterTexts(t *testing.T, card map[string]any) (symbol map[string]any, line string) {
	t.Helper()
	if _, ok := card["actions"]; ok {
		t.Fatal("redrawn card still has buttons")
	}
	body := card["body"].([]map[string]any)
	footer := body[len(body)-1]
	if footer["type"] != "ColumnSet" || footer["separator"] != true {
		t.Fatalf("footer = %v", footer)
	}
	cols := footer["columns"].([]map[string]any)
	if cols[0]["width"] != "auto" || cols[1]["width"] != "stretch" {
		t.Fatalf("columns = %v", cols)
	}
	symbol = cols[0]["items"].([]map[string]any)[0]
	return symbol, cols[1]["items"].([]map[string]any)[0]["text"].(string)
}

func TestSignoffCardRedrawnWithItsMark(t *testing.T) {
	at := time.Date(2026, 11, 17, 9, 42, 0, 0, time.UTC)
	when := "{{DATE(2026-11-17T09:42:00Z, COMPACT)}}, {{TIME(2026-11-17T09:42:00Z)}}"
	cases := []struct {
		mark         core.SignoffMark
		symbol, line string
		color        any
	}{
		{core.SignoffMark{Kind: core.SignoffMarkApproved, By: "Jane Editor", At: at}, "✓", "Jane Editor · " + when, "Good"},
		{core.SignoffMark{Kind: core.SignoffMarkCancelled, By: "Jane Editor", At: at}, "✕", "Jane Editor · " + when, "Attention"},
		{core.SignoffMark{Kind: core.SignoffMarkReplaced, At: at}, "↻", when, nil},
	}
	for _, c := range cases {
		t.Run(c.symbol, func(t *testing.T) {
			card := signoffAdaptiveCard(signoffTestCard(signoffSampleLines()...), &c.mark)
			signoffRuns(t, card)
			symbol, line := signoffFooterTexts(t, card)
			if symbol["text"] != c.symbol || symbol["color"] != c.color {
				t.Fatalf("mark = %v, want %s in %v", symbol, c.symbol, c.color)
			}
			if line != c.line {
				t.Fatalf("footer = %q, want %q", line, c.line)
			}
		})
	}
}

func signoffTeamsPlatform(t *testing.T, fs *fakeSender) (*Platform, *testSigner) {
	s := newTestSigner(t)
	p := testPlatform(s)
	p.conn = fs
	p.engaged = newEngagement("")
	p.cfg.sessionScope = "thread"
	return p, s
}

var signoffRC = replyContext{serviceURL: "https://smba.example/", conversationID: "conv-A;messageid=1"}

func signoffInvokeActivity(conv, user, value string) activity {
	return activity{
		Type:         "invoke",
		Name:         "adaptiveCard/action",
		ID:           "click-1",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "29:" + user, AADObjectID: user, Name: "Jane Editor"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: conv},
		Value:        []byte(`{"action":{"type":"Action.Execute","verb":"` + value + `"},"trigger":"manual"}`),
	}
}

// signoffPost sends a to the webhook and returns the HTTP status and the
// decoded invoke answer.
func signoffPost(t *testing.T, p *Platform, s *testSigner, claims jwt.MapClaims, a activity) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(mustJSON(a)))
	req.Header.Set("Authorization", "Bearer "+s.sign(t, claims))
	rec := httptest.NewRecorder()
	p.handleActivity(rec, req)
	var answer map[string]any
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
			t.Fatalf("answer %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, answer
}

// signoffClicks stands in for core: a click counts when count is set. With
// hold set, onClick waits for hold to close before it answers.
type signoffClicks struct {
	mu       sync.Mutex
	count    bool
	hold     chan struct{}
	got      []core.SignoffClicker
	resolved chan struct{}
}

func newSignoffClicks(count bool) *signoffClicks {
	return &signoffClicks{count: count, resolved: make(chan struct{}, 4)}
}

func (c *signoffClicks) onClick(value string, by core.SignoffClicker) (core.SignoffMark, func()) {
	c.mu.Lock()
	c.got = append(c.got, by)
	hold := c.hold
	c.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if !c.count {
		return core.SignoffMark{}, nil
	}
	kind := core.SignoffMarkApproved
	if strings.HasSuffix(value, ":cancel") {
		kind = core.SignoffMarkCancelled
	}
	return core.SignoffMark{Kind: kind, By: by.Name, At: signoffClickAt}, func() { c.resolved <- struct{}{} }
}

var signoffClickAt = time.Date(2026, 11, 17, 9, 42, 0, 0, time.UTC)

// signoffMarked fails unless the answer carries the posted card, without
// buttons, with a footer of kind by Jane Editor.
func signoffMarked(t *testing.T, answer map[string]any, kind core.SignoffMarkKind) map[string]any {
	t.Helper()
	card := signoffAnswerCard(t, answer)
	if _, ok := card["actions"]; ok {
		t.Fatalf("answered card still has buttons: %v", card["actions"])
	}
	want := signoffAdaptiveCard(signoffTestCard(signoffSampleLines()...), &core.SignoffMark{Kind: kind, By: "Jane Editor", At: signoffClickAt})
	signoffFooterTexts(t, want)
	signoffSameDrawing(t, card, want)
	return card
}

func (c *signoffClicks) clickers() []core.SignoffClicker {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]core.SignoffClicker(nil), c.got...)
}

// signoffAnswerCard returns the card of an answer that carries one.
func signoffAnswerCard(t *testing.T, answer map[string]any) map[string]any {
	t.Helper()
	if answer["statusCode"] != float64(200) || answer["type"] != adaptiveCardContentType {
		t.Fatalf("answer = %v, want 200 with a card", answer)
	}
	return answer["value"].(map[string]any)
}

// signoffSameDrawing fails unless the answered card equals want, as JSON.
func signoffSameDrawing(t *testing.T, got, want map[string]any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("answered card\n%s\nwant\n%s", g, w)
	}
}

func signoffEmpty(t *testing.T, answer map[string]any) {
	t.Helper()
	if answer["statusCode"] != float64(200) || answer["type"] != "application/vnd.microsoft.activity.message" || answer["value"] != "" {
		t.Fatalf("answer = %v, want an empty message", answer)
	}
}

func signoffGone(t *testing.T, answer map[string]any) {
	t.Helper()
	value, _ := answer["value"].(map[string]any)
	if answer["statusCode"] != float64(400) || answer["type"] != "application/vnd.microsoft.error" || value["code"] != "BadRequest" || value["message"] != "This card is no longer active." {
		t.Fatalf("answer = %v, want an error for a dead card", answer)
	}
}

func signoffPostCard(t *testing.T, p *Platform, clicks *signoffClicks) core.SignoffCardHandle {
	t.Helper()
	handle, err := p.PostSignoffCard(context.Background(), signoffRC, signoffTestCard(signoffSampleLines()...), clicks.onClick)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestSignoffPostThenRedrawEditsTheSameActivity(t *testing.T) {
	fs := &fakeSender{id: "act-9"}
	p, s := signoffTeamsPlatform(t, fs)
	clicks := newSignoffClicks(true)
	handle := signoffPostCard(t, p, clicks)
	if handle.Thread() != "conv-A;messageid=1" {
		t.Fatalf("thread = %q", handle.Thread())
	}
	if len(fs.last.Entities) != 0 {
		t.Fatalf("signoff card carries entities %v", fs.last.Entities)
	}
	mark := core.SignoffMark{Kind: core.SignoffMarkReplaced, At: time.Now()}
	if err := handle.Redraw(context.Background(), mark); err != nil {
		t.Fatal(err)
	}
	if len(fs.updatedIDs) != 1 || fs.updatedIDs[0] != "act-9" {
		t.Fatalf("updated = %v, want act-9", fs.updatedIDs)
	}

	_, answer := signoffPost(t, p, s, baseClaims(), signoffInvokeActivity("conv-A;messageid=1", "aad-1", "signoff:r1:n1"))
	signoffSameDrawing(t, signoffAnswerCard(t, answer), signoffAdaptiveCard(signoffTestCard(signoffSampleLines()...), &mark))
	if len(clicks.clickers()) != 0 {
		t.Fatal("a click on a replaced card reached onClick")
	}
}

func TestSignoffPostWithoutActivityIDFails(t *testing.T) {
	p, _ := signoffTeamsPlatform(t, &fakeSender{})
	if _, err := p.PostSignoffCard(context.Background(), signoffRC, signoffTestCard(), nil); err == nil {
		t.Fatal("post without an activity id succeeded")
	}
	if len(p.signoff.byValue) != 0 {
		t.Fatal("a card that was never posted takes clicks")
	}
}

// signoffGatedSender holds every edit until answered is closed, and notes an
// edit that waited in vain.
type signoffGatedSender struct {
	*fakeSender
	answered chan struct{}
	early    bool
}

func (g *signoffGatedSender) update(ctx context.Context, rc replyContext, activityID string, a outboundActivity) error {
	select {
	case <-g.answered:
	case <-time.After(2 * time.Second):
		g.early = true
	}
	return g.fakeSender.update(ctx, rc, activityID, a)
}

func TestSignoffInvokeIsAnsweredWithTheCardAndItsMark(t *testing.T) {
	fs := &fakeSender{id: "act-9"}
	gate := &signoffGatedSender{fakeSender: fs, answered: make(chan struct{})}
	p, s := signoffTeamsPlatform(t, fs)
	p.conn = gate
	h, got := collector()
	p.handler = h
	clicks := newSignoffClicks(true)
	signoffPostCard(t, p, clicks)

	code, answer := signoffPost(t, p, s, baseClaims(), signoffInvokeActivity("conv-A;messageid=1", "aad-1", "signoff:r1:n1"))
	close(gate.answered)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	card := signoffMarked(t, answer, core.SignoffMarkApproved)
	if c := clicks.clickers(); len(c) != 1 || c[0] != (core.SignoffClicker{ID: "aad-1", Name: "Jane Editor"}) {
		t.Fatalf("clicks = %v", c)
	}
	select {
	case <-clicks.resolved:
	case <-time.After(5 * time.Second):
		t.Fatal("the click was never resolved")
	}
	if gate.early {
		t.Fatal("the card was edited before the invoke was answered")
	}
	if len(fs.updatedIDs) != 1 || fs.updatedIDs[0] != "act-9" {
		t.Fatalf("updated = %v, want act-9 once", fs.updatedIDs)
	}
	signoffSameDrawing(t, fs.updates[0].Attachments[0].Content.(map[string]any), card)
	if len(*got) != 0 {
		t.Fatalf("the click became a message: %+v", (*got)[0])
	}

	// A second click is answered with the card as drawn and runs nothing.
	_, again := signoffPost(t, p, s, baseClaims(), signoffInvokeActivity("conv-A;messageid=1", "aad-1", "signoff:r1:n1:cancel"))
	signoffSameDrawing(t, signoffAnswerCard(t, again), card)
	if c := clicks.clickers(); len(c) != 1 {
		t.Fatalf("clicks = %v, want the first only", c)
	}
}

func TestSignoffClicksOnOneCardAskOneAtATime(t *testing.T) {
	p, s := signoffTeamsPlatform(t, &fakeSender{id: "act-9"})
	clicks := newSignoffClicks(true)
	clicks.hold = make(chan struct{})
	signoffPostCard(t, p, clicks)

	answers := make(chan map[string]any, 2)
	click := func() {
		_, answer := signoffPost(t, p, s, baseClaims(), signoffInvokeActivity("conv-A;messageid=1", "aad-1", "signoff:r1:n1"))
		answers <- answer
	}
	go click()
	deadline := time.Now().Add(5 * time.Second)
	for len(clicks.clickers()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the first click never reached onClick")
		}
		time.Sleep(5 * time.Millisecond)
	}
	go click()
	time.Sleep(100 * time.Millisecond)
	close(clicks.hold)

	for range 2 {
		select {
		case answer := <-answers:
			signoffMarked(t, answer, core.SignoffMarkApproved)
		case <-time.After(5 * time.Second):
			t.Fatal("a click was never answered")
		}
	}
	if n := len(clicks.clickers()); n != 1 {
		t.Fatalf("onClick asked %d times, want once", n)
	}
}

func TestSignoffInvokeIsReadFromTheVerb(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{name: "verb and data", value: `{"action":{"type":"Action.Execute","verb":"signoff:r1:n1","data":{"action":"signoff:r1:n1"}}}`, want: "signoff:r1:n1"},
		{name: "verb only", value: `{"action":{"type":"Action.Execute","verb":"signoff:r1:n1:cancel"}}`, want: "signoff:r1:n1:cancel"},
		{name: "data only", value: `{"action":{"type":"Action.Execute","data":{"action":"signoff:r1:n1"}}}`},
		{name: "other verb", value: `{"action":{"type":"Action.Execute","verb":"refresh","data":{"action":"signoff:r1:n1"}}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := signoffInvokeActivity("conv-A;messageid=1", "aad-1", "")
			a.Value = []byte(c.value)
			_, value, ok := signoffInvoke(mustJSON(a))
			if ok != (c.want != "") || value != c.want {
				t.Fatalf("value, ok = %q, %v; want %q", value, ok, c.want)
			}
		})
	}
}

func TestSignoffClickResolvesWhenTheEditFails(t *testing.T) {
	fs := &fakeSender{id: "act-9"}
	p, s := signoffTeamsPlatform(t, fs)
	clicks := newSignoffClicks(true)
	signoffPostCard(t, p, clicks)
	fs.err = errors.New("connector down")

	_, answer := signoffPost(t, p, s, baseClaims(), signoffInvokeActivity("conv-A;messageid=1", "aad-1", "signoff:r1:n1"))

	signoffMarked(t, answer, core.SignoffMarkApproved)
	select {
	case <-clicks.resolved:
	case <-time.After(5 * time.Second):
		t.Fatal("the click was never resolved")
	}
	if len(fs.updatedIDs) != 1 {
		t.Fatalf("updated = %v, want one edit", fs.updatedIDs)
	}
}

func TestSignoffClicksThatDoNotCount(t *testing.T) {
	open := signoffAdaptiveCard(signoffTestCard(signoffSampleLines()...), nil)
	cases := []struct {
		name   string
		count  bool
		allow  string
		claims jwt.MapClaims
		click  activity
		empty  bool
		gone   bool
	}{
		{name: "unknown card", count: true, click: signoffInvokeActivity("conv-A;messageid=1", "aad-1", "signoff:old:n0"), gone: true},
		{name: "another conversation", count: true, click: signoffInvokeActivity("conv-B", "aad-1", "signoff:r1:n1"), empty: true},
		{name: "not on allow_from", count: true, allow: "aad-editor", click: signoffInvokeActivity("conv-A;messageid=1", "aad-stranger", "signoff:r1:n1")},
		{name: "refused by core", count: false, click: signoffInvokeActivity("conv-A;messageid=1", "aad-1", "signoff:r1:n1"), empty: true},
		{name: "untrusted serviceUrl", count: true, claims: jwt.MapClaims{"serviceurl": "https://elsewhere.example/"}, click: signoffInvokeActivity("conv-A;messageid=1", "aad-1", "signoff:r1:n1"), empty: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeSender{id: "act-9"}
			p, s := signoffTeamsPlatform(t, fs)
			p.cfg.allowFrom = c.allow
			h, got := collector()
			p.handler = h
			clicks := newSignoffClicks(c.count)
			signoffPostCard(t, p, clicks)
			claims := baseClaims()
			for k, v := range c.claims {
				claims[k] = v
			}

			code, answer := signoffPost(t, p, s, claims, c.click)

			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			switch {
			case c.gone:
				signoffGone(t, answer)
			case c.empty:
				signoffEmpty(t, answer)
			default:
				signoffSameDrawing(t, signoffAnswerCard(t, answer), open)
			}
			if !c.count {
				if n := len(clicks.clickers()); n != 1 {
					t.Fatalf("onClick asked %d times, want once", n)
				}
			} else if n := len(clicks.clickers()); n != 0 {
				t.Fatalf("onClick asked %d times, want never", n)
			}
			if len(clicks.resolved) != 0 || len(fs.updatedIDs) != 0 || len(*got) != 0 {
				t.Fatalf("resolved %d, edits %v, messages %d; want none", len(clicks.resolved), fs.updatedIDs, len(*got))
			}
		})
	}
}

func TestSignoffClickFromAllowFromCounts(t *testing.T) {
	p, s := signoffTeamsPlatform(t, &fakeSender{id: "act-9"})
	p.cfg.allowFrom = "aad-editor"
	clicks := newSignoffClicks(true)
	signoffPostCard(t, p, clicks)

	_, answer := signoffPost(t, p, s, baseClaims(), signoffInvokeActivity("conv-A;messageid=1", "aad-editor", "signoff:r1:n1:cancel"))

	signoffMarked(t, answer, core.SignoffMarkCancelled)
	if c := clicks.clickers(); len(c) != 1 || c[0].ID != "aad-editor" {
		t.Fatalf("clicks = %v, want the editor's", c)
	}
}

func TestSignoffValueInAMessageIsDropped(t *testing.T) {
	p, _ := signoffTeamsPlatform(t, &fakeSender{id: "act-9"})
	h, got := collector()
	p.handler = h
	clicks := newSignoffClicks(true)
	signoffPostCard(t, p, clicks)

	submit := func(value string) activity {
		return activity{
			Type:         "message",
			ID:           "click-1",
			ServiceURL:   "https://smba.example/",
			From:         channelAccount{ID: "29:aad-1", AADObjectID: "aad-1", Name: "Jane Editor"},
			Recipient:    channelAccount{ID: "bot-1"},
			Conversation: conversationAccount{ID: "conv-A;messageid=1"},
			Value:        []byte(`{"action":"` + value + `"}`),
		}
	}
	p.dispatch(baseClaims(), mustJSON(submit("signoff:r1:n1:cancel")))
	if len(*got) != 0 || len(clicks.clickers()) != 0 {
		t.Fatalf("a signoff value in a message counted: messages %d, clicks %v", len(*got), clicks.clickers())
	}

	p.dispatch(baseClaims(), mustJSON(submit("perm:allow")))
	if len(*got) != 1 || (*got)[0].Content != "allow" || !(*got)[0].IsPermissionResponse {
		t.Fatalf("perm:allow = %+v, want a permission message", *got)
	}
}

func TestSignoffTeamsIsNoCardSender(t *testing.T) {
	var p core.Platform = &Platform{}
	if _, ok := p.(core.CardSender); ok {
		t.Fatal("Teams implements core.CardSender")
	}
	if _, ok := p.(core.SignoffCardPlatform); !ok {
		t.Fatal("Teams does not implement core.SignoffCardPlatform")
	}
}

func TestSignoffFooterLeavesOutAnEmptyName(t *testing.T) {
	_, line := signoffFooterTexts(t, signoffAdaptiveCard(signoffTestCard(), &core.SignoffMark{Kind: core.SignoffMarkApproved, At: time.Now()}))
	if strings.HasPrefix(line, " · ") {
		t.Fatalf("footer = %q", line)
	}
}
