package teams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// signoffCards keeps the posted signoff cards that still take a click, in
// memory only, by button value.
type signoffCards struct {
	mu      sync.Mutex
	byValue map[string]*signoffCard
}

type signoffCard struct {
	p          *Platform
	rc         replyContext
	activityID string
	card       *core.Card
	onClick    func(string, core.SignoffClicker)
}

// PostSignoffCard implements core.SignoffCardPlatform.
func (p *Platform) PostSignoffCard(ctx context.Context, replyCtx any, card *core.Card, onClick func(string, core.SignoffClicker)) (core.SignoffCardHandle, error) {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("teams: invalid reply context %T", replyCtx)
	}
	id, err := p.conn.send(ctx, rc, cardActivity(rc, signoffAdaptiveCard(card, nil)))
	if err != nil {
		return nil, fmt.Errorf("teams: post signoff card: %w", err)
	}
	// Without the activity id the card could never be redrawn.
	if id == "" {
		return nil, errors.New("teams: post signoff card: connector returned no activity id")
	}
	c := &signoffCard{p: p, rc: rc, activityID: id, card: card, onClick: onClick}
	p.signoff.mu.Lock()
	if p.signoff.byValue == nil {
		p.signoff.byValue = make(map[string]*signoffCard)
	}
	for _, b := range signoffButtons(card) {
		p.signoff.byValue[b.Value] = c
	}
	p.signoff.mu.Unlock()
	return c, nil
}

// Thread is the conversation the card was posted in; in a channel it names
// the thread.
func (c *signoffCard) Thread() string { return c.rc.conversationID }

// Redraw replaces the card with its text and the mark, without buttons, and
// stops routing its clicks.
func (c *signoffCard) Redraw(ctx context.Context, mark core.SignoffMark) error {
	c.p.signoff.mu.Lock()
	for _, b := range signoffButtons(c.card) {
		if c.p.signoff.byValue[b.Value] == c {
			delete(c.p.signoff.byValue, b.Value)
		}
	}
	c.p.signoff.mu.Unlock()
	if err := c.p.conn.update(ctx, c.rc, c.activityID, cardActivity(c.rc, signoffAdaptiveCard(c.card, &mark))); err != nil {
		return fmt.Errorf("teams: redraw signoff card: %w", err)
	}
	return nil
}

// handleSignoffClick takes a press on a signoff card button. A press counts
// only on an open card, from the conversation it was posted in, by someone
// allow_from lets through; anything else is logged and the card stays open.
func (p *Platform) handleSignoffClick(a *activity, value string) {
	p.signoff.mu.Lock()
	c := p.signoff.byValue[value]
	p.signoff.mu.Unlock()
	if c == nil {
		slog.Info("teams: signoff click for no open card")
		return
	}
	if a.Conversation.ID != c.rc.conversationID {
		slog.Warn("teams: signoff click from another conversation", "conversation", a.Conversation.ID)
		return
	}
	if !core.AllowList(p.cfg.allowFrom, userID(a)) {
		slog.Warn("teams: signoff click from a user not on allow_from", "user", userID(a))
		return
	}
	c.onClick(value, core.SignoffClicker{ID: userID(a), Name: a.From.Name})
}

func signoffButtons(card *core.Card) []core.CardButton {
	var buttons []core.CardButton
	for _, el := range card.Elements {
		if row, ok := el.(core.CardActions); ok {
			buttons = append(buttons, row.Buttons...)
		}
	}
	return buttons
}

// signoffAdaptiveCard draws a signoff card. Every text from the card goes into
// a TextRun, which Teams shows as written, markdown included. With a mark, the
// buttons give way to a footer with the mark.
func signoffAdaptiveCard(card *core.Card, mark *core.SignoffMark) map[string]any {
	var body []map[string]any
	if card.Header != nil {
		body = append(body, richText(map[string]any{}, map[string]any{"text": literalBraces(card.Header.Title), "size": "Large", "weight": "Bolder"}))
	}
	var previous core.CardTextStyle
	for _, el := range card.Elements {
		line, ok := el.(core.CardText)
		if !ok {
			continue
		}
		block, run := signoffLineStyle(line.Style, previous)
		run["text"] = literalBraces(line.Text)
		body = append(body, richText(block, run))
		previous = line.Style
	}
	out := adaptiveCard(body)
	if mark != nil {
		out["body"] = append(body, signoffFooter(*mark))
		return out
	}
	var actions []map[string]any
	for _, b := range signoffButtons(card) {
		action := map[string]any{
			"type":    "Action.Submit",
			"title":   literalBraces(b.Text),
			"tooltip": "",
			"data":    map[string]any{"action": b.Value},
			// Hides Teams' "Your response was sent to the app" note under the card.
			"msteams": map[string]any{"feedback": map[string]any{"hide": true}},
		}
		if b.Type == "primary" {
			action["style"] = "positive"
		}
		actions = append(actions, action)
	}
	if len(actions) > 0 {
		out["actions"] = actions
	}
	return out
}

// signoffLineStyle returns the block and run attributes of a line in style,
// following a line in previous.
func signoffLineStyle(style, previous core.CardTextStyle) (block, run map[string]any) {
	block, run = map[string]any{}, map[string]any{}
	switch style {
	case core.CardTextHeading:
		block["separator"] = true
		block["spacing"] = "Medium"
		run["size"] = "Small"
		run["weight"] = "Bolder"
		run["isSubtle"] = true
	case core.CardTextName:
		run["weight"] = "Bolder"
	case core.CardTextDetail:
		block["spacing"] = "None"
		run["size"] = "Small"
		run["isSubtle"] = true
	case core.CardTextLink:
		block["spacing"] = "None"
		if previous == core.CardTextDetail {
			block["spacing"] = "Small"
		}
		run["size"] = "Small"
		run["color"] = "Accent"
	}
	return block, run
}

// literalBraces puts a word joiner (U+2060) between the two braces of every
// "{{", so Teams shows {{DATE(…)}} and {{TIME(…)}} as written. The joiner has
// no width.
func literalBraces(s string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	var previous rune
	for _, r := range s {
		if r == '{' && previous == '{' {
			b.WriteRune('\u2060')
		}
		b.WriteRune(r)
		previous = r
	}
	return b.String()
}

func richText(block, run map[string]any) map[string]any {
	run["type"] = "TextRun"
	block["type"] = "RichTextBlock"
	block["inlines"] = []map[string]any{run}
	return block
}

// signoffFooter is the line under a resolved card: the mark in a narrow
// column, then who clicked and when. Teams fills in the date and time in each
// viewer's locale and time zone.
func signoffFooter(mark core.SignoffMark) map[string]any {
	symbol := map[string]any{"type": "TextBlock", "size": "Medium", "weight": "Bolder"}
	switch mark.Kind {
	case core.SignoffMarkApproved:
		symbol["text"], symbol["color"] = "✓", "Good"
	case core.SignoffMarkCancelled:
		symbol["text"], symbol["color"] = "✕", "Attention"
	default:
		symbol["text"], symbol["isSubtle"] = "↻", true
	}
	at := mark.At.UTC().Format(time.RFC3339)
	when := "{{DATE(" + at + ", COMPACT)}}, {{TIME(" + at + ")}}"
	line := when
	if mark.Kind != core.SignoffMarkReplaced && strings.TrimSpace(mark.By) != "" {
		line = mark.By + " · " + when
	}
	return map[string]any{
		"type":      "ColumnSet",
		"separator": true,
		"spacing":   "Medium",
		"columns": []map[string]any{
			{"type": "Column", "width": "auto", "verticalContentAlignment": "Center", "items": []map[string]any{symbol}},
			{"type": "Column", "width": "stretch", "verticalContentAlignment": "Center", "items": []map[string]any{
				{"type": "TextBlock", "text": line, "size": "Small", "isSubtle": true, "wrap": true},
			}},
		},
	}
}
