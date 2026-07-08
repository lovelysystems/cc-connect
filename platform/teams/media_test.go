package teams

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestImageActivity_BuildsDataURIAttachment(t *testing.T) {
	rc := replyContext{
		conversationID: "c1",
		botAccount:     channelAccount{ID: "bot"},
		userAccount:    channelAccount{ID: "user"},
	}
	data := []byte{0x89, 0x50, 0x4e, 0x47}
	a := imageActivity(rc, core.ImageAttachment{MimeType: "image/png", Data: data, FileName: "chart.png"})

	if len(a.Attachments) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(a.Attachments))
	}
	att := a.Attachments[0]
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if att.ContentUrl != want {
		t.Errorf("contentUrl = %q, want %q", att.ContentUrl, want)
	}
	if att.ContentType != "image/png" || att.Name != "chart.png" {
		t.Errorf("contentType/name = %q/%q", att.ContentType, att.Name)
	}
	// Conversation-reference envelope carried through.
	if a.Type != "message" || a.From == nil || a.From.ID != "bot" || a.Recipient == nil || a.Recipient.ID != "user" {
		t.Errorf("envelope missing: %+v", a)
	}
}

func TestImageActivity_DefaultsMimeAndName(t *testing.T) {
	a := imageActivity(replyContext{}, core.ImageAttachment{Data: []byte{1, 2, 3}})
	att := a.Attachments[0]
	if att.ContentType != "image/png" || att.Name != "image.png" {
		t.Errorf("defaults not applied: contentType=%q name=%q", att.ContentType, att.Name)
	}
	if !strings.HasPrefix(att.ContentUrl, "data:image/png;base64,") {
		t.Errorf("contentUrl prefix wrong: %q", att.ContentUrl)
	}
}
