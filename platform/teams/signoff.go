package teams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/golang-jwt/jwt/v5"
)

// signoffCards keeps the posted signoff cards, in memory only, by button
// value.
type signoffCards struct {
	mu      sync.Mutex
	byValue map[string]*signoffCard
}

type signoffCard struct {
	p          *Platform
	rc         replyContext
	activityID string
	card       *core.Card
	onClick    func(string, core.SignoffClicker) (core.SignoffMark, func())
	// clickMu lets one click at a time ask onClick and record its mark.
	clickMu sync.Mutex
	// mark is set once the card is resolved; guarded by signoffCards.mu.
	mark *core.SignoffMark
}

// invokeResponse is the body of the answer to an adaptiveCard/action invoke.
type invokeResponse struct {
	StatusCode int    `json:"statusCode"`
	Type       string `json:"type"`
	Value      any    `json:"value"`
}

// signoffEmptyAnswer answers a click on a card cc-connect cannot draw: a
// message with no text, which leaves the card as it is.
var signoffEmptyAnswer = invokeResponse{StatusCode: http.StatusOK, Type: "application/vnd.microsoft.activity.message", Value: ""}

func signoffCardAnswer(card map[string]any) invokeResponse {
	return invokeResponse{StatusCode: http.StatusOK, Type: adaptiveCardContentType, Value: card}
}

// PostSignoffCard implements core.SignoffCardPlatform.
func (p *Platform) PostSignoffCard(ctx context.Context, replyCtx any, card *core.Card, onClick func(string, core.SignoffClicker) (core.SignoffMark, func())) (core.SignoffCardHandle, error) {
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

// Redraw replaces the card with its text and the mark, without buttons. Later
// clicks on it are answered with that drawing and never reach onClick.
func (c *signoffCard) Redraw(ctx context.Context, mark core.SignoffMark) error {
	c.p.signoff.mu.Lock()
	c.mark = &mark
	c.p.signoff.mu.Unlock()
	if err := c.p.conn.update(ctx, c.rc, c.activityID, cardActivity(c.rc, signoffAdaptiveCard(c.card, &mark))); err != nil {
		return fmt.Errorf("teams: redraw signoff card: %w", err)
	}
	return nil
}

// handleSignoffClick takes a press on a signoff card button from an
// adaptiveCard/action invoke and returns the invoke's answer. A press counts
// only on an open card, from the conversation it was posted in, by someone
// allow_from lets through: it is answered with the card drawn with its mark,
// and resolve is to be called once the answer is written. resolve edits the
// posted card to the same drawing, then runs the rest of the click. Any other
// press is logged, runs nothing and is answered with the card as drawn now, or
// with signoffEmptyAnswer when cc-connect holds no card it may draw there.
func (p *Platform) handleSignoffClick(claims jwt.MapClaims, a *activity, value string) (answer invokeResponse, resolve func()) {
	if !p.serviceURLTrusted(claims, a) {
		return signoffEmptyAnswer, nil
	}
	p.signoff.mu.Lock()
	c := p.signoff.byValue[value]
	p.signoff.mu.Unlock()
	if c == nil {
		slog.Info("teams: signoff click for no held card; answered with an empty message")
		return signoffEmptyAnswer, nil
	}
	// The card's text stays out of other conversations.
	if a.Conversation.ID != c.rc.conversationID {
		slog.Warn("teams: signoff click from another conversation; answered with an empty message", "conversation", a.Conversation.ID)
		return signoffEmptyAnswer, nil
	}
	if !core.AllowList(p.cfg.allowFrom, userID(a)) {
		slog.Warn("teams: signoff click from a user not on allow_from; answered with the card as drawn", "user", userID(a))
		return signoffCardAnswer(c.drawn()), nil
	}

	c.clickMu.Lock()
	defer c.clickMu.Unlock()
	p.signoff.mu.Lock()
	resolved := c.mark != nil
	p.signoff.mu.Unlock()
	if resolved {
		slog.Info("teams: signoff click on a resolved card; answered with the card as drawn")
		return signoffCardAnswer(c.drawn()), nil
	}
	mark, rest := c.onClick(value, core.SignoffClicker{ID: userID(a), Name: a.From.Name})
	if rest == nil {
		return signoffCardAnswer(c.drawn()), nil
	}
	p.signoff.mu.Lock()
	c.mark = &mark
	p.signoff.mu.Unlock()
	return signoffCardAnswer(signoffAdaptiveCard(c.card, &mark)), func() {
		// The invoke answer reaches only the client that clicked; the edit
		// shows the mark on every other client.
		if err := c.Redraw(context.Background(), mark); err != nil {
			slog.Error("teams: redraw clicked signoff card failed", "conversation", c.rc.conversationID, "error", err)
		}
		rest()
	}
}

// drawn is the card as Teams shows it now.
func (c *signoffCard) drawn() map[string]any {
	c.p.signoff.mu.Lock()
	mark := c.mark
	c.p.signoff.mu.Unlock()
	return signoffAdaptiveCard(c.card, mark)
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
		// A click is answered with the redrawn card, so Teams swaps it in one
		// step on the clicking client; the posted card is also updated so
		// every other client shows it.
		action := map[string]any{
			"type":    "Action.Execute",
			"title":   literalBraces(b.Text),
			"tooltip": "",
			"verb":    b.Value,
			"data":    map[string]any{"action": b.Value},
			// Hides Teams' "Your response was sent to the app" note under the card in the desktop app.
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
