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
	onOutcome  func(report string)
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
func (p *Platform) PostGateCard(ctx context.Context, sessionKey string, replyCtx any, card core.GateCard, onOutcome func(string)) error {
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
	p.gates[card.RequestID] = &openGate{card: card, sessionKey: sessionKey, rc: rc, activityID: id, onOutcome: onOutcome}
	p.gatesMu.Unlock()
	slog.Info("teams: gate card posted", "request_id", card.RequestID, "activity_id", id, "session", sessionKey)
	return nil
}

func gatePromptCard(card core.GateCard) map[string]any {
	label := card.ConfirmLabel
	if label == "" {
		label = "Confirm"
	}
	value := gateActionPrefix + card.RequestID + ":" + card.Nonce
	c := gateCardBody(card, "")
	c["actions"] = actionSet([]cardButton{
		{title: label, action: value},
		{title: "Cancel", action: value + gateCancelSuffix},
	})
	return c
}

func gateCardBody(card core.GateCard, status string) map[string]any {
	body := []map[string]any{
		{"type": "TextBlock", "text": card.Title, "wrap": true, "weight": "Bolder", "size": "Medium"},
		{"type": "TextBlock", "text": card.Body, "wrap": true},
	}
	if status != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": status, "wrap": true, "separator": true})
	}
	return adaptiveCard(body)
}

// handleGateClick consumes a gate button click. It reports false for any other
// action, and true for every gate: value, valid or not, so a gate click never
// becomes a message for the engine.
func (p *Platform) handleGateClick(a *activity, action string) bool {
	rest, ok := strings.CutPrefix(action, gateActionPrefix)
	if !ok {
		return false
	}
	rest, cancel := strings.CutSuffix(rest, gateCancelSuffix)
	requestID, nonce, ok := strings.Cut(rest, ":")
	if !ok || strings.Contains(nonce, ":") {
		slog.Warn("teams: malformed gate action", "action", action)
		return true
	}
	if !core.AllowList(p.cfg.allowFrom, userID(a)) {
		slog.Warn("teams: gate click from unauthorized user", "user", userID(a))
		return true
	}

	p.gatesMu.Lock()
	g := p.gates[requestID]
	if g == nil || g.card.Nonce != nonce {
		p.gatesMu.Unlock()
		slog.Warn("teams: gate click for unknown or resolved card", "request_id", requestID)
		return true
	}
	delete(p.gates, requestID)
	p.gatesMu.Unlock()

	clicker := gateClicker{EntraObjectID: a.From.AADObjectID, Name: a.From.Name}
	slog.Info("teams: gate click", "request_id", requestID, "cancel", cancel, "clicked_by", clicker.EntraObjectID)

	outcome, message := "cancelled", "Cancelled by "+clicker.Name+"."
	if !cancel {
		outcome, message = p.runGateCommand(gateCommandInput{RequestID: requestID, Nonce: nonce, ClickedBy: clicker})
	}

	var status string
	switch outcome {
	case "published":
		status = "Published by " + clicker.Name + "."
	case "cancelled":
		status = "Cancelled by " + clicker.Name + "."
	default:
		status = "Not published: " + message
	}
	if err := p.conn.update(context.Background(), g.rc, g.activityID, cardActivity(g.rc, gateCardBody(g.card, status))); err != nil {
		slog.Error("teams: gate card edit failed", "request_id", requestID, "activity_id", g.activityID, "error", err)
	}
	if g.onOutcome != nil {
		g.onOutcome(fmt.Sprintf("Gate report for request %s: outcome %q, clicked by %s. Message: %s",
			requestID, outcome, clicker.Name, message))
	}
	return true
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
