package teams

import "testing"

func TestCleanText_StripsMention(t *testing.T) {
	a := &activity{
		Text: "<at>mybot</at> hello there",
		Entities: []entity{
			{Type: "mention", Text: "<at>mybot</at>", Mentioned: channelAccount{ID: "bot-1"}},
		},
	}
	if got := a.cleanText(); got != "hello there" {
		t.Fatalf("cleanText = %q, want %q", got, "hello there")
	}
}

func TestMentionsBot(t *testing.T) {
	a := &activity{
		Recipient: channelAccount{ID: "bot-1"},
		Entities: []entity{
			{Type: "mention", Mentioned: channelAccount{ID: "bot-1"}},
		},
	}
	if !a.mentionsBot("bot-1") {
		t.Error("expected mentionsBot true when bot id mentioned")
	}

	// Recipient fallback: a mention whose Mentioned.ID matches the activity
	// recipient counts even if the passed botID differs.
	if !a.mentionsBot("some-other-id") {
		t.Error("expected mentionsBot true via recipient fallback")
	}

	// No recipient, mention is another user → not a bot mention.
	b := &activity{Entities: []entity{{Type: "mention", Mentioned: channelAccount{ID: "someone"}}}}
	if b.mentionsBot("bot-1") {
		t.Error("expected mentionsBot false when only another user is mentioned")
	}
}

func TestCleanText_MultipleMentionsAndNonMention(t *testing.T) {
	a := &activity{
		Text: "<at>bot</at> ping <at>alice</at> please",
		Entities: []entity{
			{Type: "mention", Text: "<at>bot</at>", Mentioned: channelAccount{ID: "bot-1"}},
			{Type: "mention", Text: "<at>alice</at>", Mentioned: channelAccount{ID: "alice"}},
			{Type: "clientInfo"}, // non-mention entity must be ignored, not panic
		},
	}
	if got := a.cleanText(); got != "ping  please" {
		t.Fatalf("cleanText = %q, want %q", got, "ping  please")
	}
}

func TestCardAction_Variants(t *testing.T) {
	cases := map[string]string{
		`{"action":"act:/x"}`: "act:/x",
		`{"cmd":"pause"}`:     "pause",
		`{"action":123}`:      "", // non-string value ignored
		`{"action":{}}`:       "",
		`not json`:            "",
		`{}`:                  "",
	}
	for raw, want := range cases {
		a := &activity{Value: []byte(raw)}
		if got := a.cardAction(); got != want {
			t.Errorf("cardAction(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestCardAction(t *testing.T) {
	a := &activity{Value: []byte(`{"action":"act:/heartbeat pause"}`)}
	if got := a.cardAction(); got != "act:/heartbeat pause" {
		t.Fatalf("cardAction = %q", got)
	}
	none := &activity{}
	if got := none.cardAction(); got != "" {
		t.Fatalf("cardAction = %q, want empty", got)
	}
}

func TestSessionKey_Scopes(t *testing.T) {
	a := &activity{Conversation: conversationAccount{ID: "conv-9"}, From: channelAccount{ID: "user-7"}}
	cases := map[string]string{
		"user":    "teams:conv-9:user-7",
		"thread":  "teams:conv-9",
		"channel": "teams:conv-9",
	}
	for scope, want := range cases {
		p := &Platform{cfg: config{sessionScope: scope}}
		if got := p.sessionKey(a); got != want {
			t.Errorf("scope %q: sessionKey = %q, want %q", scope, got, want)
		}
	}
}
