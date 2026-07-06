package teams

import (
	"strings"
	"testing"
)

func firstTextBlock(card map[string]any) map[string]any {
	body, _ := card["body"].([]map[string]any)
	if len(body) == 0 {
		return nil
	}
	return body[0]
}

func TestLoadingCard(t *testing.T) {
	c := loadingCard("💭 Working…")
	if c["type"] != "AdaptiveCard" || c["version"] != "1.5" {
		t.Fatalf("not an AdaptiveCard 1.5: %v", c)
	}
	tb := firstTextBlock(c)
	txt, _ := tb["text"].(string)
	if tb["type"] != "TextBlock" || !strings.Contains(txt, "💭 Working…") {
		t.Errorf("loading text block = %v", tb)
	}
	// grayed + small + italic (markdown)
	if tb["isSubtle"] != true || tb["size"] != "Small" {
		t.Errorf("loading text should be subtle+Small, got %v", tb)
	}
	if !strings.HasPrefix(txt, "_") || !strings.HasSuffix(txt, "_") {
		t.Errorf("loading text should be italic markdown, got %q", txt)
	}
}

func TestLoadingCard_EmptyFallsBackToDefault(t *testing.T) {
	tb := firstTextBlock(loadingCard(""))
	if txt, _ := tb["text"].(string); !strings.Contains(txt, defaultCardWorkingText) {
		t.Errorf("empty text -> %v, want it to contain default %q", tb["text"], defaultCardWorkingText)
	}
}

func TestAnswerCard_SingleTextBlock(t *testing.T) {
	c := answerCard("hello **world**")
	body, _ := c["body"].([]map[string]any)
	if len(body) != 1 {
		t.Fatalf("answer card should be one text block (no body footer), got %d", len(body))
	}
	if body[0]["text"] != "hello **world**" || body[0]["wrap"] != true {
		t.Errorf("answer block = %v", body[0])
	}
}

func TestAIGeneratedEntity(t *testing.T) {
	e := aiGeneratedEntity()
	if e["type"] != "https://schema.org/Message" || e["@type"] != "Message" {
		t.Errorf("entity envelope = %v", e)
	}
	at, _ := e["additionalType"].([]string)
	if len(at) != 1 || at[0] != "AIGeneratedContent" {
		t.Errorf("additionalType = %v, want [AIGeneratedContent]", e["additionalType"])
	}
}

func TestCardActivity_WrapsAttachment(t *testing.T) {
	a := cardActivity(replyContext{conversationID: "c1"}, loadingCard("hi"))
	if a.Type != "message" || len(a.Attachments) != 1 {
		t.Fatalf("card activity = %+v", a)
	}
	if a.Attachments[0].ContentType != adaptiveCardContentType {
		t.Errorf("contentType = %q", a.Attachments[0].ContentType)
	}
}
