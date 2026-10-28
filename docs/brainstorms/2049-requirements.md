# Teams: dropped attachments — unit brief

Unit: `teams-dropped-attachments`, one pull request into `downstream`.

Story: lovelysystems/ch#2049
Workpackage: ch#2049 WP1
Task: lovelysystems/ch#2125

An image an editor attaches in a Teams channel reaches the agent even when SharePoint is still
saving it, and when an attachment cannot be read after all, the agent knows it was dropped and why.

## Acceptance criteria this unit serves

From lovelysystems/ch#2049, verbatim:

- [ ] Known chat issues relevant for wave 1 are fixed

Every sub-issue of the story counts as relevant. This unit closes two of them: ch#1661 and ch#1662.

## The slice

### ch#1661 — an attached image is retried before it is declared unreadable

Observed on `op-stg-radio`, 2026-09-07 11:06:23 UTC, session `s93`: an image attached in a Teams
channel got `404` from the Graph content GET
(`…/drives/<id>/root:/Local%20Test/profile_nextgen-web-herolandscape-1920x.jpeg:/content`). The bot
logged `teams: some inbound attachments were skipped count=1`, posted the warning, and ran the turn
with empty content. Re-sending the identical file about 70 s later worked, and SharePoint stored it
as `…-1920x 1.jpeg` — the duplicate-name suffix, so the first upload had landed after all. The
first GET came before SharePoint had finished committing the file.

Where it happens today: `platform/teams/graph.go` `downloadFile` (three Graph calls: site, drive,
content) and `getBounded`, which gives up on the first non-2xx; called per file from
`platform/teams/webhook.go` `downloadChannelFiles`. The 1:1 path (`connector.go` `fetch`, called
from `downloadInboundMedia`) also gives up on the first non-2xx.

Expected: an attachment that is still being committed is retried before it is declared
unreadable, so the editor does not have to send the same image twice.

Cases:

- The channel file's content GET fails at first and succeeds on a later attempt → the file reaches
  the agent, no warning is posted.
- Every attempt fails → the file is skipped and the warning is posted, as today.
- The file is over the size cap → no retry; skipped with the warning, as today.
- The retries never delay the webhook acknowledgement; downloads already run on the async dispatch
  goroutine.
- A message with several attachments where one keeps failing → the others still arrive.

### ch#1662 — the agent is told when an attachment could not be read

Observed in the same thread, 2026-09-07 11:07:07 UTC: after the dropped attachment the turn was
dispatched with empty content and no sign that a download failed. Asked "what is wrong with the
attachment exactly?", the agent answered that no attachment had reached it — one message after the
bot itself had posted the warning.

Where it happens today: `platform/teams/webhook.go`, where `failed > 0` posts
`attachmentFailureNotice` to the editor; the `core.Message` handed on carries only what succeeded.

Expected: the agent is told an attachment was dropped and why, so it can say that instead of
contradicting the bot's own warning.

Cases:

- An attachment fails and the message has no text → the agent still receives a message that says
  an attachment could not be read, and why (too large, or unavailable).
- Some attachments succeed and one fails → the agent gets the files that succeeded plus the note.
- Everything succeeds → no note.
- Both the 1:1 path and the channel path carry the note.

## The decisions this brief prefers

- Every sub-issue of the story is fixed before wave 1; both issues here are in.
- The editor-facing warning (`attachmentFailureNotice`) keeps its wording; the change is what the
  agent is told.
- Both fixes stay inside `platform/teams/`.

## Constraints from outside this repo

- **Branch and pull request.** `downstream` is the deployed branch: an upstream release base plus
  Lovely's commits. `main` is a clean mirror of upstream and never receives Lovely commits. This
  pull request targets `downstream`, and every diff, rebase and comparison is against
  `origin/downstream`.
- **Changelog.** Each fix gets an entry in `CHANGES.md`, under `## v1.5.0` → `### Unreleased` →
  `#### Fix`, in the form `- **teams**: <what changed> (upstream: pending; follow-up to
  chenhg5/cc-connect#1518)`. `CHANGELOG.md` and `README.md` belong to upstream and are not edited.
- **Upstreamable commits.** The Teams connector is submitted upstream as chenhg5/cc-connect#1518.
  The changes follow upstream conventions with their own tests, so they can be folded into it.
- **Consumers.** The bot image `ch-content-bot` runs this bridge with Teams options set in its
  `docker/config.default.toml`, among them `channel_files_enabled = true`; `max_attachment_bytes` is
  left at its default. Its runtime image pins the bridge by release tag; the release is cut
  separately.

## What to leave alone

- `core/` and every other platform: shared with upstream, outside this unit.
- `platform/teams/streaming_card.go` and the silent-reply (`NO_REPLY`) handling: the empty card is
  handled in the bot image, not here.
- `Makefile:VERSION`, tags, and cutting a fork release: a separate step after merge.
- `README.md`, `CHANGELOG.md`, `DOWNSTREAM.md`.
- The stale remote branches (`fix/quiet-streaming-card*`, `feature/teams-*`): do not build on them.
- The rest of the story, handled in other repos: the GitHub token retry in `lovely-docker-agent`,
  the skill fixes in `ch-content-bot`, the generator, deploy config and alerting.

## Capture instruction

> Whenever you make a choice this brief and the unit plan do not dictate, and a reasonable
> implementer could have gone the other way, append one line to
> `docs/brainstorms/teams-dropped-attachments-decisions.md`: what you chose, the alternative you
> rejected, one clause of why, and `file:line`. Write it as you go, never reconstructed at the end —
> a decision looks inevitable once the code around it exists, which is exactly how it escapes
> notice.

Planning writes what it pinned that this brief did not under `## Planning`; implementation writes
what it decided while building under `## Implementation`. One file, one list.

## When to ask and when to decide

> Ask when being wrong is expensive beyond this unit; log and continue when it is contained within
> it. Concretely, at the moment a choice appears: if this brief or the requirements resolve it, it
> is not a decision — do it. If either option would contradict them, stop: that is a requirements
> defect, not a choice. If choosing wrong would cost work outside this unit — it crosses a unit
> boundary, touches anything on the tripwire list, or becomes visible outside the system at release
> — stop: write the question under `## Open questions` in `docs/brainstorms/teams-dropped-attachments-decisions.md`,
> commit and push, and end the run. Otherwise decide, log one line, and carry on.
>
> What binds is the acceptance criteria, the outcomes the requirements state, and the tripwire list.
> Where this brief or the requirements name a mechanism — which component does the work, which
> fields, how many, validated how — that is a preference, not a settled decision. Follow it unless
> the code shows it is wrong: it contradicts what the code already does, or it cannot reach the
> outcome it serves. Then do not build it and do not quietly swap it: name the `file:line` that
> shows it, write the question under `## Open questions` in the decisions file, commit and push,
> and end the run. Dislike of a preference is not evidence; the code is.

"The requirements" here means this brief, the story's acceptance criteria and the two issues
quoted above.

### Tripwire list

- Any change outside `platform/teams/` — `core/` and other platforms are shared with upstream.
- The text an editor sees in Teams: `attachmentFailureNotice` or any new message posted to the chat.
- The Teams platform config keys: adding, renaming or removing one needs a matching change in the
  bot image's config.
- Microsoft Graph or Bot Framework permissions: anything beyond what the bot holds today
  (`Sites.Selected` and the RSC manifest permission for channel files, `docs/teams.md:93`) needs
  the customer's Azure admin.
