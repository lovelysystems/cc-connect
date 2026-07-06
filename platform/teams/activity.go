package teams

import (
	"encoding/json"
	"strings"
)

// activity is the subset of a Bot Framework Activity the connector consumes.
// See https://learn.microsoft.com/azure/bot-service/rest-api/bot-framework-rest-connector-api-reference.
type activity struct {
	Type         string              `json:"type"`
	ID           string              `json:"id"`
	Text         string              `json:"text"`
	ServiceURL   string              `json:"serviceUrl"`
	ReplyToID    string              `json:"replyToId"`
	From         channelAccount      `json:"from"`
	Recipient    channelAccount      `json:"recipient"`
	Conversation conversationAccount `json:"conversation"`
	Entities     []entity            `json:"entities"`
	Value        json.RawMessage     `json:"value"`
}

type channelAccount struct {
	ID          string `json:"id"`
	AADObjectID string `json:"aadObjectId"`
	Name        string `json:"name"`
}

type conversationAccount struct {
	ID               string `json:"id"`
	ConversationType string `json:"conversationType"`
	Name             string `json:"name"`
}

// entity is a Bot Framework entity; only mention entities are interpreted.
type entity struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Mentioned channelAccount `json:"mentioned"`
}

func parseActivity(body []byte) (*activity, error) {
	var a activity
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// cleanText returns the message text with bot @mention markup removed. Teams
// includes the mention's display text inline (e.g. "<at>bot</at> hi"); each
// mention entity carries the exact Text span to strip.
func (a *activity) cleanText() string {
	text := a.Text
	for _, e := range a.Entities {
		if strings.EqualFold(e.Type, "mention") && e.Text != "" {
			text = strings.ReplaceAll(text, e.Text, "")
		}
	}
	return strings.TrimSpace(text)
}

// mentionsBot reports whether the activity @mentions this bot (by the bot's app
// ID, which equals the activity recipient ID).
func (a *activity) mentionsBot(botID string) bool {
	for _, e := range a.Entities {
		if strings.EqualFold(e.Type, "mention") &&
			(e.Mentioned.ID == botID || (a.Recipient.ID != "" && e.Mentioned.ID == a.Recipient.ID)) {
			return true
		}
	}
	return false
}

// hasMention reports whether the activity carries any @mention entity (targeting
// the bot or anyone else). Used by the engaged-thread follow filter to ignore
// messages addressed to other participants.
func (a *activity) hasMention() bool {
	for _, e := range a.Entities {
		if strings.EqualFold(e.Type, "mention") {
			return true
		}
	}
	return false
}

// cardAction returns the action string from a card submit (Action.Submit), or
// "" when the activity is not a card action. Teams delivers submits as a message
// activity carrying `value` and (usually) no text.
func (a *activity) cardAction() string {
	if len(a.Value) == 0 {
		return ""
	}
	var v map[string]any
	if err := json.Unmarshal(a.Value, &v); err != nil {
		return ""
	}
	for _, key := range []string{"action", "cmd"} {
		if s, ok := v[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
