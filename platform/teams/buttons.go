package teams

import (
	"context"
	"fmt"

	"github.com/chenhg5/cc-connect/core"
)

// SendWithButtons implements core.InlineButtonSender. The engine calls it for
// interactive prompts (tool permission gates and single-select AskUserQuestion)
// before falling back to plain text. When a streaming card is live for the
// conversation, the prompt and its buttons are folded into that card in place;
// otherwise a standalone Adaptive Card carrying the buttons is sent (threaded to
// the originating activity like a text Reply). The button Data (e.g. "perm:allow",
// "askq:0:1") rides in each Action.Submit so cardAction() resolves it inbound.
func (p *Platform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error {
	rc, ok := replyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("teams: invalid reply context %T", replyCtx)
	}
	cbs := flattenButtons(buttons)

	if card, ok := p.activeCard(rc.conversationID); ok {
		return card.promptUpdate(ctx, content, cbs)
	}

	a := cardActivity(rc, promptCard(content, cbs))
	if rc.activityID != "" {
		return p.conn.replyTo(ctx, rc, rc.activityID, a)
	}
	_, err := p.conn.send(ctx, rc, a)
	return err
}

// flattenButtons maps the engine's button-grid to a flat []cardButton (Teams
// renders Action.Submit as a single action list; the engine's row grouping is
// not preserved).
func flattenButtons(rows [][]core.ButtonOption) []cardButton {
	var out []cardButton
	for _, row := range rows {
		for _, b := range row {
			out = append(out, cardButton{title: b.Text, action: b.Data})
		}
	}
	return out
}
