---
title: Teams card buttons that redraw on click — Action.Execute, invoke answer, and a PUT
date: 2026-11-20
category: design-patterns
module: platform/teams
problem_type: design_pattern
component: messaging
severity: medium
applies_when:
  - A Teams Adaptive Card button must change the card the moment it is clicked
  - Choosing between Action.Submit and Action.Execute for a Teams card
  - Hiding Teams' "Your response was sent to the app" note under a card
  - Putting user- or caller-supplied text into a Teams TextRun
retire_when: "Teams starts delivering an adaptiveCard/action invoke answer to every client in the conversation, or hides the web client's feedback note for Action.Execute; check the Universal Action Model and User Specific Views pages on learn.microsoft.com"
tags: [teams, adaptive-cards, action-execute, action-submit, invoke, signoff-cards, feedback-hide]
---

# Teams card buttons that redraw on click — Action.Execute, invoke answer, and a PUT

## Context

Signoff cards (PR #9, open at the time of writing) needed a button click to swap the card for a
resolved drawing at once. The first build used `Action.Submit`, the way every other Teams card in
cc-connect does (`platform/teams/card.go:60`): the webhook acks the message activity with
202 and the card is redrawn later by a PUT on its activity. In a Teams test the clicked button
stayed held, both buttons went blank for about a second, and then the redrawn card appeared.

The unit brief preferred `Action.Submit` because "`Action.Execute` does not hide" the feedback note.
That belief came from a proof-of-concept Execute card that never put `msteams.feedback.hide` on the
action itself; the flag only worked for Submit after the proof of concept moved it onto the action.
With the flag on the action, Execute hides the note in the desktop client too.

## Guidance

For a button whose click must redraw the card:

- Use `Action.Execute` with the value in `verb`, and put
  `"msteams": {"feedback": {"hide": true}}` on the action object (`platform/teams/signoff.go:207-214`).
  Card `version` stays `1.5`, which Teams asks for with Universal Actions (`platform/teams/card.go:85`).
- Route the `adaptiveCard/action` invoke on `verb` alone (`platform/teams/webhook.go:237`); a `data`
  object on the button is unread weight.
- Answer the invoke in the webhook's HTTP response with the redrawn card:
  `{"statusCode":200,"type":"application/vnd.microsoft.card.adaptive","value":<card>}`
  (`platform/teams/webhook.go:91-100`). Teams swaps the card in one step on the clicking client.
- Start anything that posts to the thread (placeholder, follow-up turn) only after the answer is
  written, on its own goroutine; posting earlier races the invoke answer to the thread
  (`platform/teams/webhook.go:97-99`).
- Also edit the card's activity by PUT with the same drawing after the answer
  (`platform/teams/signoff.go:150-157`). The invoke answer reaches only the client that clicked —
  User Specific Views: "The User Specific Card returned by the bot is sent only to the specific client
  that requested for it". Without the PUT, other viewers and the clicker's other devices keep the
  buttons. A failed PUT is logged and the click goes on.
- Answers that leave the card as it is:
  - card as drawn now (`signoffCardAnswer(c.drawn())`) when cc-connect holds the card but the click
    does not count;
  - empty message `{"statusCode":200,"type":"application/vnd.microsoft.activity.message","value":""}`
    when no held card may be drawn there, or when the card is about to be redrawn by someone else;
  - invoke error `{"statusCode":400,"type":"application/vnd.microsoft.error","value":{"code":"BadRequest","message":"…"}}`
    when the clicker should see that the card is dead (`platform/teams/signoff.go:46-52`). Teams
    shows it as an error. As read while building this, 400 with that type is the only 4xx row in
    the Universal Action Model response table, and `BadRequest` is the code the Bot Framework SDK
    sends from its own `adaptiveCard/action` handler.

Text going into a `TextRun`:

- Teams substitutes `{{DATE(…)}}` and `{{TIME(…)}}` inside a `TextRun`. Put a word joiner (U+2060)
  between the braces to keep them literal (`platform/teams/signoff.go:254-271`).
- A line break inside one text splits it onto a second line on the card. Refuse control characters
  in text that must stay one line.

Known limit: the Teams web client still shows "Your response was sent to the app" under an
`Action.Execute` card with the hide flag on the action; the desktop client does not, and
`Action.Submit` hides it in both. Execute was kept for the instant, flash-free swap.

## Why This Matters

- Submit plus a 202 ack plus a later PUT shows a held button and blank buttons for about a second
  before the redraw. Execute with the card in the invoke answer has no such gap.
- An invoke answer alone looks right to the clicker and wrong to everyone else in the channel: the
  Teams test showed the web client still holding the buttons after a desktop click.
- Reading the brief's "Execute does not hide the note" as a Teams fact would have kept the
  flicker; the flag's placement was the cause.

## When to Apply

- Any new Teams card whose button should resolve the card in place.
- Moving the permission and AskUserQuestion cards (`Action.Submit`, `platform/teams/card.go:43-66`)
  to an in-place redraw.
- Not needed for a button whose click only posts a reply and leaves the card unchanged; Submit is
  enough there.

## Examples

Button, before (Submit, flag placement that did not hide the note in the proof of concept):

```json
{"type": "Action.Submit", "title": "Approve", "data": {"action": "signoff:…"}}
```

Button, after:

```json
{
  "type": "Action.Execute",
  "title": "Approve",
  "verb": "signoff:…",
  "msteams": {"feedback": {"hide": true}}
}
```

Click handling, after (`platform/teams/webhook.go:91-100`):

```go
if a, value, ok := signoffInvoke(body); ok {
    answer, resolve := p.handleSignoffClick(claims, a, value)
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(answer) // redrawn card for the clicking client; error log omitted
    if resolve != nil {
        go resolve() // PUT the same drawing for every other client, then the follow-up work
    }
    return
}
```

## Related

- `docs/teams.md` — "Signoff cards" section
- https://learn.microsoft.com/en-us/adaptive-cards/authoring-cards/universal-action-model
- https://learn.microsoft.com/en-us/microsoftteams/platform/task-modules-and-cards/cards/universal-actions-for-adaptive-cards/user-specific-views
