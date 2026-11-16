package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/golang-jwt/jwt/v5"
)

const (
	gateActionPrefix = "gate:"
	gateCancelSuffix = ":cancel"
	gateCommandLimit = 60 * time.Second
)

// openGate is a posted gate card waiting for its one click. Kept in memory only.
type openGate struct {
	card       core.GateCard
	sessionKey string
	rc         replyContext
	activityID string
	onClick    core.GateClick
}

// gateClick is a valid click on an open gate card, which it has taken out of
// the open set.
type gateClick struct {
	gate    *openGate
	nonce   string
	cancel  bool
	clicker gateClicker
}

// gateClicker is who clicked, as handed to the gate command.
type gateClicker struct {
	EntraObjectID string `json:"entra_object_id"`
	Name          string `json:"name"`
}

type gateCommandInput struct {
	RequestID string      `json:"request_id"`
	Nonce     string      `json:"nonce"`
	ClickedBy gateClicker `json:"clicked_by"`
}

type gateCommandOutput struct {
	Outcome string `json:"outcome"`
	Message string `json:"message"`
}

// PostGateCard implements core.GateCardPoster.
func (p *Platform) PostGateCard(ctx context.Context, sessionKey string, replyCtx any, card core.GateCard, onClick core.GateClick) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("teams: invalid reply context %T", replyCtx)
	}
	id, err := p.conn.send(ctx, rc, cardActivity(rc, gatePromptCard(card)))
	if err != nil {
		return fmt.Errorf("teams: post gate card: %w", err)
	}
	if id == "" {
		return fmt.Errorf("teams: post gate card: connector returned no activity id")
	}
	p.gatesMu.Lock()
	if p.gates == nil {
		p.gates = make(map[string]*openGate)
	}
	p.gates[card.RequestID] = &openGate{card: card, sessionKey: sessionKey, rc: rc, activityID: id, onClick: onClick}
	p.gatesMu.Unlock()
	slog.Info("teams: gate card posted", "request_id", card.RequestID, "activity_id", id, "session", sessionKey)
	return nil
}

func gateConfirmLabel(card core.GateCard) string {
	if card.ConfirmLabel == "" {
		return "Confirm"
	}
	return card.ConfirmLabel
}

func gatePromptCard(card core.GateCard) map[string]any {
	value := gateActionPrefix + card.RequestID + ":" + card.Nonce
	c := gateCardBody(card, "")
	c["actions"] = []map[string]any{
		gateExecuteAction(gateConfirmLabel(card), value),
		gateExecuteAction("Cancel", value+gateCancelSuffix),
	}
	return c
}

func gateExecuteAction(title, value string) map[string]any {
	return map[string]any{
		"type":  "Action.Execute",
		"title": title,
		"verb":  value,
		"data":  map[string]any{"action": value},
	}
}

func gateCardBody(card core.GateCard, status string) map[string]any {
	body := []map[string]any{
		{"type": "TextBlock", "text": card.Title, "wrap": true, "weight": "Bolder", "size": "Medium"},
		{"type": "TextBlock", "text": card.Body, "wrap": true},
	}
	if status != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": status, "wrap": true, "separator": true})
	}
	c := adaptiveCard(body)
	// Action.Execute needs version 1.4 or later.
	c["version"] = "1.4"
	return c
}

// clickedCard is the gate card after c: the action taken and who took it, no buttons.
func (c *gateClick) clickedCard() map[string]any {
	label := gateConfirmLabel(c.gate.card)
	if c.cancel {
		label = "Cancel"
	}
	return gateCardBody(c.gate.card, "**"+label+"** — by "+c.clicker.Name)
}

// claimGate validates a gate action and takes its card out of the open set.
// isGate is false for any other action; click is nil for a gate action that is
// malformed, unauthorized, or for a card that is unknown or already resolved.
func (p *Platform) claimGate(a *activity, action string) (click *gateClick, isGate bool) {
	rest, ok := strings.CutPrefix(action, gateActionPrefix)
	if !ok {
		return nil, false
	}
	rest, cancel := strings.CutSuffix(rest, gateCancelSuffix)
	requestID, nonce, ok := strings.Cut(rest, ":")
	if !ok || strings.Contains(nonce, ":") {
		slog.Warn("teams: malformed gate action", "action", action)
		return nil, true
	}
	if !core.AllowList(p.cfg.allowFrom, userID(a)) {
		slog.Warn("teams: gate click from unauthorized user", "user", userID(a))
		return nil, true
	}

	p.gatesMu.Lock()
	g := p.gates[requestID]
	if g == nil || g.card.Nonce != nonce {
		p.gatesMu.Unlock()
		slog.Warn("teams: gate click for unknown or resolved card", "request_id", requestID)
		return nil, true
	}
	delete(p.gates, requestID)
	p.gatesMu.Unlock()

	clicker := gateClicker{EntraObjectID: a.From.AADObjectID, Name: a.From.Name}
	slog.Info("teams: gate click", "request_id", requestID, "cancel", cancel, "clicked_by", clicker.EntraObjectID)
	return &gateClick{gate: g, nonce: nonce, cancel: cancel, clicker: clicker}, true
}

// handleGateClick consumes an Action.Submit gate button click. It reports false
// for any other action, and true for every gate: value, valid or not, so a gate
// click never becomes a message for the engine.
func (p *Platform) handleGateClick(a *activity, action string) bool {
	c, isGate := p.claimGate(a, action)
	if c == nil {
		return isGate
	}
	onOutcome := c.hold()
	g := c.gate
	if err := p.conn.update(context.Background(), g.rc, g.activityID, cardActivity(g.rc, c.clickedCard())); err != nil {
		slog.Error("teams: gate card edit failed", "request_id", g.card.RequestID, "activity_id", g.activityID, "error", err)
	}
	p.resolveGate(c, onOutcome)
	return true
}

// gateInvokeResponse answers an adaptiveCard/action invoke.
type gateInvokeResponse struct {
	StatusCode int    `json:"statusCode"`
	Type       string `json:"type"`
	Value      any    `json:"value"`
}

const gateInvokeIgnoredText = "This card no longer takes this action."

// handleCardInvoke answers an adaptiveCard/action invoke from an Action.Execute
// button. ok is false for any other activity. A click on an open gate card is
// answered with the clicked card, and then resolves the click; anything else
// gets a message and leaves the card as it is.
func (p *Platform) handleCardInvoke(claims jwt.MapClaims, body []byte) (response gateInvokeResponse, then func(), ok bool) {
	a, err := parseActivity(body)
	if err != nil || !strings.EqualFold(a.Type, "invoke") || !strings.EqualFold(a.Name, "adaptiveCard/action") {
		return gateInvokeResponse{}, nil, false
	}
	ignored := gateInvokeResponse{StatusCode: 200, Type: "application/vnd.microsoft.activity.message", Value: gateInvokeIgnoredText}
	if !p.serviceURLTrusted(claims, a) {
		return ignored, nil, true
	}
	c, _ := p.claimGate(a, a.invokeAction())
	if c == nil {
		return ignored, nil, true
	}
	onOutcome := c.hold()
	return gateInvokeResponse{StatusCode: 200, Type: adaptiveCardContentType, Value: c.clickedCard()},
		func() { p.resolveGate(c, onOutcome) }, true
}

// hold tells the engine about the click, which holds the card's session until
// the outcome turn is done. It returns the outcome callback, nil when the card
// has none.
func (c *gateClick) hold() func(string, core.StreamingCard) {
	if c.gate.onClick == nil {
		return nil
	}
	return c.gate.onClick()
}

// resolveGate posts the outcome turn's placeholder, runs the gate command on
// Confirm, and hands the outcome to onOutcome.
func (p *Platform) resolveGate(c *gateClick, onOutcome func(string, core.StreamingCard)) {
	if onOutcome == nil {
		return
	}
	g := c.gate
	requestID := g.card.RequestID
	placeholder, err := p.createCardStream(context.Background(), g.rc)
	if err != nil {
		slog.Error("teams: gate placeholder post failed", "request_id", requestID, "error", err)
	}
	outcome, message := "cancelled", "Cancelled by "+c.clicker.Name+"."
	if !c.cancel {
		outcome, message = p.runGateCommand(gateCommandInput{RequestID: requestID, Nonce: c.nonce, ClickedBy: c.clicker})
	}
	onOutcome(fmt.Sprintf("Gate report for request %s: outcome %q, clicked by %s. Message: %s",
		requestID, outcome, c.clicker.Name, message), placeholder)
}

// runGateCommand runs gate_command with in as JSON on stdin and reads the
// outcome from its stdout. Any failure yields outcome "failed".
func (p *Platform) runGateCommand(in gateCommandInput) (outcome, message string) {
	if p.cfg.gateCommand == "" {
		return "failed", "gate_command is not configured"
	}
	stdin, err := json.Marshal(in)
	if err != nil {
		return "failed", "encode gate input: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), gateCommandLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.cfg.gateCommand)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		slog.Error("teams: gate command failed", "error", err, "stderr", strings.TrimSpace(stderr.String()))
		return "failed", "gate command failed: " + err.Error()
	}
	var out gateCommandOutput
	if err := json.Unmarshal(stdout, &out); err != nil {
		return "failed", "gate command printed invalid JSON: " + err.Error()
	}
	if out.Outcome != "published" && out.Outcome != "refused" {
		return "failed", fmt.Sprintf("gate command returned unknown outcome %q", out.Outcome)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out.Message), "\n")
	return out.Outcome, first
}
