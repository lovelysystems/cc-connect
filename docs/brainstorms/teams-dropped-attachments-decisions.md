# Teams dropped attachments — decisions

## Planning

- Retry both paths (1:1 `connector.fetch` and the channel content GET), not the channel path alone: the brief lists both as giving up on the first non-2xx — `platform/teams/connector.go:157`, `platform/teams/graph.go:237`.
- Retry transport errors and 404/408/409/423/429/5xx, not 400/401/403: a missing `Sites.Selected` grant answers 403 and retrying it only delays the warning — `platform/teams/graph.go:205`.
- Retry around `graph.downloadFile` in the dispatch loop, which re-runs site and drive lookups, rather than inside `downloadFile` around the content GET only: the loop is reachable from the `fakeGraph` dispatch tests — `platform/teams/webhook.go:258`.
- Waits 1 s and 3 s within a 6 s budget, chosen by the user (Taras) in review, not 5/10/20/40 s with 75 s, 5 s + 15 s, or 2 s + 5 s with 10 s: the editor sees nothing during retries, so a file that never arrives costs ~4 s of silence, tuned later from the `teams: retrying inbound attachment download` log line — `platform/teams/webhook.go:32`.
- One 6 s budget per message measured as wall-clock time since its first download attempt, not a wait schedule per attachment or a sum of waits: bounds when the last retry can start even when attempts hit the 30 s client timeout — `platform/teams/webhook.go:60`, `platform/teams/graph.go:18`, `platform/teams/connector.go:17`.
- Wait schedule and budget as package variables overridden in tests, not an injected clock: smallest seam the package needs — `platform/teams/webhook.go:188`.
- Agent note in `core.Message.ExtraContent`, not appended to `Content`: the engine prepends it and still runs command and alias checks on the user text alone — `core/engine.go:2877`.
- Note as an English literal in `platform/teams/`, not an i18n key: `core/i18n.go` is outside this unit and the note is agent-facing, like the existing literal notice — `platform/teams/webhook.go:24`.
- Refs beyond `maxChannelFileRefs` reported as "unavailable" with a too-many-files detail, not a third top-level reason: keeps the brief's two reasons while saying they were never attempted — `platform/teams/webhook.go:253`.
- Malformed 1:1 file attachment reported as "unavailable", not a third reason: the editor-facing notice already groups it there — `platform/teams/webhook.go:200`.

## Implementation

- One `statusOutcome` helper in the connector file used by both download paths, not a classifier per path: the retryable status set is the same for SharePoint and Bot Connector URLs — `platform/teams/connector.go:156`.
- A body read error mid-download is retryable, not failed: it is a transport failure like a dropped connection — `platform/teams/connector.go:197`, `platform/teams/graph.go:261`.
- Token-acquisition failure stays `fetchFailed`, not retryable: a token error is not the SharePoint commit lag this retry targets — `platform/teams/connector.go:176`.
- Retry as a `retryDownload` function taking the message deadline, not a retrier type: both loops only need a deadline and a closure — `platform/teams/webhook.go:313`.
- Inline 1:1 images fetched over HTTP get the same retry as files; `data:` URIs return `fetchFailed` and are never retried: one path for every network download — `platform/teams/webhook.go:245`.
- One Info log per retry with the file name and wait, not silent retries: an operator can see a slow commit before the warning fires — `platform/teams/webhook.go:319`.
- Retry and the agent note land in one commit, not separate ones: both rewrite the same download loops and return values — `platform/teams/webhook.go:212`, `platform/teams/webhook.go:270`.
- Download functions return `[]droppedAttachment` (name, reason), not the count plus a parallel reason list: the dispatch log keeps its `count` from `len(dropped)` — `platform/teams/webhook.go:43`.
- Note is one bracketed line naming each file in quotes with its reason, not a multi-line list: it is prepended to the user's text by the engine — `platform/teams/webhook.go:339`.
- An attachment without a name appears as "unnamed attachment" in the note, not omitted: inline 1:1 images often carry no name — `platform/teams/webhook.go:344`.
- The note does not say the user saw the warning: the notice reply can fail, and the note would then be wrong — `platform/teams/webhook.go:198`.
- Over-cap reason text "unavailable: too many files in one message, only the first N are read", with N from `maxChannelFileRefs`: says why without a third reason — `platform/teams/webhook.go:287`.
- Tests shrink `retryWaits`/`retryBudget` through a `fastRetries` helper, and the fakes gained scripted per-URL outcomes, not new fake types: smallest change to the existing `fakeGraph`/`fakeSender` — `platform/teams/session_test.go`, `platform/teams/reply_test.go`.
- Site and drive lookups retry on transport errors and 408/409/423/429/5xx like the content GET, but a lookup 404 fails, not retries: a 404 there means a wrong site or drive (chosen by Taras in review, accepting the review finding) — `platform/teams/graph.go:223`.

## Open questions
