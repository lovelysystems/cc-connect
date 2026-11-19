package teams

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
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

	actions, _ := card["actions"].([]map[string]any)
	if len(actions) != 2 {
		t.Fatalf("actions = %v, want two buttons", card["actions"])
	}
	for i, a := range actions {
		hide := a["msteams"].(map[string]any)["feedback"].(map[string]any)["hide"]
		if a["type"] != "Action.Submit" || hide != true || a["tooltip"] != "" {
			t.Fatalf("action %d = %v", i, a)
		}
	}
	if actions[0]["title"] != "Approve" || actions[0]["style"] != "positive" || actions[0]["data"].(map[string]any)["action"] != "signoff:r1:n1" {
		t.Fatalf("confirm = %v", actions[0])
	}
	if actions[1]["title"] != "Cancel" || actions[1]["style"] != nil || actions[1]["data"].(map[string]any)["action"] != "signoff:r1:n1:cancel" {
		t.Fatalf("cancel = %v", actions[1])
	}
}

func TestSignoffCardKeepsMarkupAsWritten(t *testing.T) {
	texts := []string{"**x**", "<b>x</b>", `{"type":"AdaptiveCard"}`, "{{DATE(2026-11-17T10:42:00Z, COMPACT)}}"}
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

func signoffTeamsPlatform(fs *fakeSender) *Platform {
	p := teamsPlatform("thread")
	p.conn = fs
	return p
}

var signoffRC = replyContext{serviceURL: "https://smba.example/", conversationID: "conv-A;messageid=1"}

func signoffClick(conv, user, value string) []byte {
	return mustJSON(activity{
		Type:         "message",
		ID:           "click-1",
		ServiceURL:   "https://smba.example/",
		From:         channelAccount{ID: "29:" + user, AADObjectID: user, Name: "Jane Editor"},
		Recipient:    channelAccount{ID: "bot-1"},
		Conversation: conversationAccount{ID: conv},
		Value:        []byte(`{"action":"` + value + `"}`),
	})
}

type signoffClicks struct{ got []core.SignoffClicker }

func (c *signoffClicks) onClick(value string, by core.SignoffClicker) {
	c.got = append(c.got, by)
}

func TestSignoffPostThenRedrawEditsTheSameActivity(t *testing.T) {
	fs := &fakeSender{id: "act-9"}
	p := signoffTeamsPlatform(fs)
	handle, err := p.PostSignoffCard(context.Background(), signoffRC, signoffTestCard(signoffSampleLines()...), nil)
	if err != nil {
		t.Fatal(err)
	}
	if handle.Thread() != "conv-A;messageid=1" {
		t.Fatalf("thread = %q", handle.Thread())
	}
	if len(fs.last.Entities) != 0 {
		t.Fatalf("signoff card carries entities %v", fs.last.Entities)
	}
	if err := handle.Redraw(context.Background(), core.SignoffMark{Kind: core.SignoffMarkApproved, By: "Jane Editor", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if len(fs.updatedIDs) != 1 || fs.updatedIDs[0] != "act-9" {
		t.Fatalf("updated = %v, want act-9", fs.updatedIDs)
	}
	if len(p.signoff.byValue) != 0 {
		t.Fatal("a redrawn card still takes clicks")
	}
}

func TestSignoffPostWithoutActivityIDFails(t *testing.T) {
	p := signoffTeamsPlatform(&fakeSender{})
	if _, err := p.PostSignoffCard(context.Background(), signoffRC, signoffTestCard(), nil); err == nil {
		t.Fatal("post without an activity id succeeded")
	}
	if len(p.signoff.byValue) != 0 {
		t.Fatal("a card that was never posted takes clicks")
	}
}

func TestSignoffClickReachesTheCardNeverTheAgent(t *testing.T) {
	p := signoffTeamsPlatform(&fakeSender{id: "act-9"})
	h, got := collector()
	p.handler = h
	clicks := &signoffClicks{}
	if _, err := p.PostSignoffCard(context.Background(), signoffRC, signoffTestCard(), clicks.onClick); err != nil {
		t.Fatal(err)
	}

	p.dispatch(nil, signoffClick("conv-A;messageid=1", "aad-1", "signoff:r1:n1"))

	if len(clicks.got) != 1 || clicks.got[0] != (core.SignoffClicker{ID: "aad-1", Name: "Jane Editor"}) {
		t.Fatalf("clicks = %v", clicks.got)
	}
	if len(*got) != 0 {
		t.Fatalf("the click became a message: %+v", (*got)[0])
	}
}

func TestSignoffClickFromAnotherConversationRunsNothing(t *testing.T) {
	p := signoffTeamsPlatform(&fakeSender{id: "act-9"})
	h, got := collector()
	p.handler = h
	clicks := &signoffClicks{}
	if _, err := p.PostSignoffCard(context.Background(), signoffRC, signoffTestCard(), clicks.onClick); err != nil {
		t.Fatal(err)
	}

	p.dispatch(nil, signoffClick("conv-B", "aad-1", "signoff:r1:n1"))

	if len(clicks.got) != 0 || len(*got) != 0 {
		t.Fatalf("clicks %v, messages %d; want none", clicks.got, len(*got))
	}
}

func TestSignoffClickOnlyFromAllowFrom(t *testing.T) {
	p := signoffTeamsPlatform(&fakeSender{id: "act-9"})
	p.cfg.allowFrom = "aad-editor"
	h, got := collector()
	p.handler = h
	clicks := &signoffClicks{}
	if _, err := p.PostSignoffCard(context.Background(), signoffRC, signoffTestCard(), clicks.onClick); err != nil {
		t.Fatal(err)
	}

	p.dispatch(nil, signoffClick("conv-A;messageid=1", "aad-stranger", "signoff:r1:n1"))
	if len(clicks.got) != 0 || len(*got) != 0 {
		t.Fatalf("a click from outside allow_from counted: clicks %v, messages %d", clicks.got, len(*got))
	}

	p.dispatch(nil, signoffClick("conv-A;messageid=1", "aad-editor", "signoff:r1:n1"))
	if len(clicks.got) != 1 || clicks.got[0].ID != "aad-editor" {
		t.Fatalf("clicks = %v, want the editor's", clicks.got)
	}
}

func TestSignoffUnknownValueIsSwallowed(t *testing.T) {
	p := signoffTeamsPlatform(&fakeSender{id: "act-9"})
	h, got := collector()
	p.handler = h

	p.dispatch(nil, signoffClick("conv-A;messageid=1", "aad-1", "signoff:old:n0:cancel"))
	if len(*got) != 0 {
		t.Fatalf("an unknown signoff value became a message: %+v", (*got)[0])
	}

	p.dispatch(nil, signoffClick("conv-A;messageid=1", "aad-1", "perm:allow"))
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
