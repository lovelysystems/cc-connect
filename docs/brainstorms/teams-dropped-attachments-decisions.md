# Teams dropped attachments — decisions

## Planning

- Retry both paths (1:1 `connector.fetch` and the channel content GET), not the channel path alone: the brief lists both as giving up on the first non-2xx — `platform/teams/connector.go:157`, `platform/teams/graph.go:237`.
- Retry transport errors and 404/408/409/423/429/5xx, not 400/401/403: a missing `Sites.Selected` grant answers 403 and retrying it only delays the warning — `platform/teams/graph.go:205`.
- Retry around `graph.downloadFile` in the dispatch loop, which re-runs site and drive lookups, rather than inside `downloadFile` around the content GET only: the loop is reachable from the `fakeGraph` dispatch tests and only the content step reports retryable — `platform/teams/webhook.go:258`.
- Waits 5/10/20/40 s (75 s), not a shorter schedule: the observed lag was ~70 s — `docs/brainstorms/2049-requirements.md`.
- One 75 s budget per message measured as wall-clock time since its first download attempt, not a wait schedule per attachment or a sum of waits: bounds when the last retry can start even when attempts hit the 30 s client timeout — `platform/teams/webhook.go:60`, `platform/teams/graph.go:18`, `platform/teams/connector.go:17`.
- Wait schedule and budget as package variables overridden in tests, not an injected clock: smallest seam the package needs — `platform/teams/webhook.go:188`.
- Agent note in `core.Message.ExtraContent`, not appended to `Content`: the engine prepends it and still runs command and alias checks on the user text alone — `core/engine.go:2877`.
- Note as an English literal in `platform/teams/`, not an i18n key: `core/i18n.go` is outside this unit and the note is agent-facing, like the existing literal notice — `platform/teams/webhook.go:24`.
- Refs beyond `maxChannelFileRefs` reported as "unavailable" with a too-many-files detail, not a third top-level reason: keeps the brief's two reasons while saying they were never attempted — `platform/teams/webhook.go:253`.
- Malformed 1:1 file attachment reported as "unavailable", not a third reason: the editor-facing notice already groups it there — `platform/teams/webhook.go:200`.

## Implementation

## Open questions
