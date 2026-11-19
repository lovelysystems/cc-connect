# signoff-cards — decisions

Plan: `docs/plans/2026-11-19-1449-feat-signoff-cards-plan.md`. One line per choice: what, the rejected alternative, why, where.

## Plan

### Read by the gate in ch-content-bot

- Style names `heading`, `name`, `detail`, `link`; over `title`/`subtitle`-style or platform words; they name what the brief's four line kinds are — plan KTD1, `core/card.go:28-76` (new element beside these).
- Body line shape `{"style": "...", "text": "..."}` with non-empty text; over `[style, text]` pairs; named keys decode strictly and read clearly — plan KTD2.
- `clicked_by` inner names `id` (Teams: Entra object id) and `name` (display name); over the proof of concept's `entra_object_id`; core stays platform-neutral — plan KTD8, `platform/teams/webhook.go:554-559`.
- Time limit for `signoff_exec`: fixed 5 min, not configurable; over 60 s (cron default, the proof of concept) or a config key; a push plus follow-up may outlast 60 s, 5 min is the webhook exec limit, and no key is added — plan KTD8, `core/webhook.go:257`.
- A cc-connect stop or the limit kills the command's process group and gives no report, so the gate's publish must be all-or-nothing; over a context of its own that claimed to survive a stop; a service stop kills the unit's processes anyway — plan KTD8, `daemon/systemd.go:179-190`.
- A gate started inside a cron or timer job that runs in a new session per run is refused (its agent is a side session the route does not resolve); over resolving side sessions; nothing needs scheduled signoffs — `core/engine.go:1621-1625`, `core/engine.go:1822-1826`.
- Stdout strictness: exactly one JSON object, unknown fields refused, `message` optional and one line (a line break gives `failed`); over taking the first line as the proof of concept did; the brief says anything else is `failed` — plan KTD8.
- Answer `200 {"status":"posted"}`; refusals as plain text with 400 (input), 403 (caller), 404 (no live session), 422 (no `run_as_user`, platform without the interface); over JSON error bodies; the socket's other routes answer errors with `http.Error` — plan KTD2, `core/api.go:205`.
- Unknown request fields refused (strict decode), `project` included; over accepting `/send`'s optional `project`; the brief allows exactly seven fields and the caller check picks the engine — plan KTD2, KTD3, `core/api.go:225-249`.
- `request_id` and `nonce` must not contain `:`; over any string; the button value is split on `:` — plan KTD2.
- Cancel value `signoff:<request_id>:<nonce>:cancel`; over a separate `signoff-cancel:` prefix; one prefix keeps one intercept — plan KTD2.
- Report text `Signoff <request_id>: <outcome>` (`replaced` adds ` by <newer request_id>`), second line the command's `message`; over the proof of concept's sentence with the clicker's name; the brief asks for the outcome and the message, and the newer id tells the agent a card is still open — plan KTD10.
- The gate learns `session_key` on its side; under `run_as_user`, `CC_SESSION_KEY` reaches the agent only when `run_as_env` lists it; documented, not changed — `core/runas.go:102-108`, `core/engine.go:4059-4076`.

### Mechanism

- Engine found from `session_key` (engines with a live session), the caller check picking among several; over a `project` field; keeps the brief's seven fields and the check separable — `core/engine.go:16467-16478`.
- Caller check on Linux only (`SO_PEERCRED` + `/proc`), failing closed elsewhere; over adding `golang.org/x/sys` for darwin; the bot runs in a Linux pod and `x/sys` is only indirect — `go.mod:98`.
- Agent pid through a new optional `AgentProcessReporter`; over a field on `AgentSession`; optional capability interfaces are the house pattern — `core/engine.go:5598`.
- Claude Code's `Pid()` in a new file `agent/claudecode/session_pid.go`; over adding it to `session.go` as the proof of concept did; stays out of PR #8's hunks — `agent/claudecode/session.go:31`.
- Platform interface returns a handle (redraw, thread); over a platform registry plus a handler setter; mirrors `StreamingCardPlatform` returning `StreamingCard` — `core/interfaces.go:698-715`.
- Value (nonce) check in core, thread check in Teams; over both in Teams as in the proof of concept; core rule stays platform-neutral, only Teams knows conversation ids — `platform/teams/webhook.go:161-168`.
- Open and pending cards on the engine; over `interactiveState`; ~30 teardown paths discard that state — `core/engine.go:4229-4290`.
- Replacement keyed by the thread the platform posted into; over the session key; channel and user scopes make session keys differ from threads — `platform/teams/session.go:132-143`.
- Post the newer card first, void older ones only after it succeeds; over void-then-post; a failed post would leave the thread with no open card — plan KTD5.
- Posting on the first of final reply or `Session.Released()`; over flushing when `processInteractiveEvents` returns; a request between the turn's last post and its unlock would be stranded — `core/engine.go:3866`, `core/engine.go:6298-6305`.
- Card posts with the requesting turn's reply context; over `ReconstructReplyCtx`; the card lands in the thread the turn answered — `core/engine.go:1724-1729`.
- Session reservation as a FIFO lock hand-off, with continuation and drain yielding to it; over `waitForSessionLock` polling or `Released()`-then-`TryLock`; both leave a gap a typed message wins — `core/engine.go:2771-2785`, `core/engine.go:5957`.
- Messages queued before the click also wait until after the report turn; over answering them first; one ordering rule, and the brief's case is about messages after the click — `core/engine.go:6298`.
- `signoff_exec` runs at the click, not after the hold is taken; over running it under the hold; publication does not wait for an unrelated running turn — plan KTD8.
- `signoff_exec` built from shell and flag without `shell_profile`; over the shared helper as is; the profile may source agent-writable files and root would run them — `core/engine.go:7842-7845`.
- Working directory `/`; over `work_dir`; a root shell never resolves paths in a directory the agent's user writes — plan KTD8.
- Process-group kill on the limit; over killing the direct process as `killAndWait` does; the gate's children must not publish after `failed` — `core/engine.go:8084-8089`.
- `signoff_exec` on the engine's context with `WaitDelay` 2 s; over a context of its own; a stop kills it either way, and `WaitDelay` keeps an escaped descendant holding a pipe from wedging the hold — `core/hooks.go:180`.
- `signoff_exec` unset gives `failed` at Approve; over refusing the route; one less refusal, visible at once — plan Scope Boundaries.
- Report turn as a new path sharing an extracted timer resolution helper; over a `HeldSession` field on `TimerJob` as in the proof of concept; the brief asks for a path beside timers — `core/engine.go:1781-1809`.
- Report text in English, not i18n; over a `MsgKey`; it is a prompt to the agent, not a string a person reads — `core/i18n.go:160`.
- Placeholder handed over on `Message.Placeholder` at the end of the struct; over parking it on `interactiveState`; a parked card could be adopted by a later typed turn — `core/message.go:363-402`.
- A placeholder the report turn never reaches is finalized empty; over the proof of concept's fixed failure sentence; the brief allows no words of cc-connect's own — plan KTD9.
- `/ps` refused while a hold has no turn in flight; over leaving it; its output could become the report turn's result — `core/engine.go:6438-6444`.
- Idle close cancelled when the hold is taken; over leaving the timer armed; it could close the agent while the command runs — `core/engine.go:4293`.
- After `/new`, `/cancel` or `/switch` the report turn runs on the new active session; over the held old one; two turns would run on one agent — `core/engine.go:6943`, `core/engine.go:10150`.
- Click intercept after the serviceURL checks and before the `allow_from` check, `onClick` only for a clicker `allow_from` passes; over any thread member's click counting; the user's answer at plan review — `platform/teams/webhook.go:123-153`.
- Every `signoff:` value swallowed, card found or not; over passing unknown ones on; a stale `:cancel` would read as the deny keyword — `platform/teams/webhook.go:191-193`, `core/engine.go:3653-3674`.
- `onClick` returns after claim and redraw, the rest on a goroutine; over running the hold inline; Teams dispatch has 16 slots — `platform/teams/teams.go:30`.
- A Teams post with no activity id is a failed post; over keeping a card it cannot redraw; `sendTo` treats the id as best-effort — `platform/teams/connector.go:133-141`.
- Request text drawn as `TextRun` in `RichTextBlock`; over `TextBlock`; `TextBlock` text is markdown — `platform/teams/card.go:3-5`.
- If Teams substitutes `{{DATE(`/`{{TIME(` in a `TextRun`, a zero-width word joiner goes between the two `{` in the drawn text only; over refusing such text or stopping the build; the text still looks as written — plan KTD6.
- Footer as `TextBlock` with `{{DATE}}`/`{{TIME}}`, clicker's name not escaped; over a `TextRun` for the name; Teams formats date and time in `TextBlock`, the name comes from the tenant directory — `platform/teams/card.go:12-19`.
- Card buttons keep Teams' default left alignment, confirm first; over right-aligned Cancel-first; matches the brief's sample and the mockups — `platform/teams/card.go:53-66`.
- The route declares `SignoffCardPlatform` and the engine's accept entry point (U3), U4 fills them in; over U4 owning them; U3's tests need the seam — plan U3.
- `CHANGES.md` gets a new `#### Feature` block under the empty `### Unreleased`, after PR #8's `#### Fix` at the rebase; over claiming no overlap; the two blocks start at the same line — `CHANGES.md:18`.
- Route and `signoff_exec` documented in `config.example.toml`, the card in `docs/teams.md`; over `docs/usage.md`; no doc lists socket routes and `docs/usage.md` is a PR #8 file — `config.example.toml:735-760`.
- Mockups adapted from the lovely-cockpit mockups (content, click line option C, replaced option B), headings sent upper-case in the request; over CSS upper-casing; Adaptive Cards cannot change case — `sandbox/claude/tv-build/signoff-cards-mockups/` (not committed).

## Plan review

Sources read: brief, this log, ch#2022 and ch#2023 with comments, task ch#2320 (no comments), workpackage plan `ch-engineering` `origin/master:plans/ch-2022-…/workpackages.gdoc.md` at `synced_at` 2026-11-17T17:58:14Z (WP1, WP5), `ch-content-bot` and `op-argo-apps` at `origin/master`.

### Cuts

- Cut: the `CardText` case in `Card.RenderText` and its test (KTD1, U1); nothing in the story requires it now — a signoff card never goes through the text fallback (brief, "The card"), and the switch skips unknown elements (`core/card.go:237-268`).

### Other changes

- U6 execution note now follows KTD6 and the `## Plan` line above: if Teams substitutes `{{DATE(`/`{{TIME(` in a `TextRun`, add the word joiner and log it; the note said "record it and stop", which contradicted both.
- KTD7 and KTD10 carry a pointer to the two open questions below.

### Departures from the brief, checked

- Click intercept before `allow_from` (KTD7, R14): follows "Anyone in the thread may click" literally; not settled for a deployment with `allow_from` — logged under `## Open questions`, not passed.
- Replacement keyed on the thread the card was posted into, over the session key (KTD5): the brief says "in that thread"; the content bot runs `session_scope = "thread"` (`ch-content-bot` `origin/master:docker/config.default.toml`), where the two are equal, and `platform/teams/session.go:125-143` shows they differ only in the other scopes; kept, nothing the gate reads.
- `signoff_exec` without `shell_profile`, from `/` (KTD8), over "as `exec` is elsewhere" (`shellExecCommand` prepends the profile, `core/engine.go:7842-7845`; `work_dir` is the agent's): kept; the tripwire "anything cc-connect runs … as root on a path the agent's user can write" requires it; the gate reads it from `config.example.toml` (U7) and must use absolute paths; the content bot sets no `shell_profile` today.
- Session reservation as a FIFO lock hand-off (KTD12, U8), beyond the brief's "a new path beside timers": kept; the tripwire "an outcome turn dropped because the session is busy; the click holds the session" needs it, and the existing drain pops messages queued after the click before it unlocks (`core/engine.go:6298-6325`), so the 20 ms poll (`core/engine.go:2771-2785`) loses; internal, nothing the gate reads. Traced to WP1 "cc-connect reports each card's outcome to the bot as a new turn" and WP5 "The bot tells the editor … each card's outcome".
- Messages queued before the click also wait for the report turn (KTD12), beyond the brief's case about messages after the click: kept; it falls out of the hand-off, and answering them first needs a before/after marker on each queued message.
- Fixed 5 min limit that kills the process group, and no report on a stop (KTD8): kept; the brief has this unit pin the limit and log it ("the time limit — the gate reads from this repo"), the group kill is what makes a `failed` report true, and open cards are memory-only by the brief, so nothing reports after a restart. The gate's publish must be all-or-nothing; WP5's single push of one commit is. Documented in U7.
- Report turn on the new active session after `/new`, `/cancel` or `/switch` (KTD9), over the brief's "on the session the click holds": kept; on the old one two turns would run on one agent (`core/engine.go:6943`, `core/engine.go:10150`).
- Refusal 404 for a `session_key` with no live session (KTD2), beyond the brief's refusal list: kept; it is the engine lookup the caller check needs, and no live session means no agent to descend from.
- Outcome report text `Signoff <request_id>: <outcome>` (KTD10): adds the gate's id to a tripwire-listed contract — logged under `## Open questions`, not passed.

### User's answers

- Signoff click and `allow_from`: option B; every `signoff:` value is still consumed first, but only a clicker on `allow_from` reaches `onClick`, anyone else's click is logged and the card stays open (KTD7, R14, U6, U7).
- Outcome report text: option A; `Signoff <request_id>: <outcome>` with ` by <newer request_id>` on `replaced` and the `message` on a second line, now a pinned contract the gate reads (KTD10, "Pinned for the gate", U7).

## Build

- Report turn finds agent, session manager and interactive key through `sessionContextForKey` and `interactiveKeyForSessionKey`, and posts with the card's own platform and reply context; over extracting `ExecuteTimerJob`'s resolution into a helper both share; the card already carries the platform and thread the timer derives from the session key, the lookup is the route's own, and timers stay untouched — `core/signoff.go:404`, `core/engine.go:16382`.
- Report message has no `UserID`; over a sender like the timer's `"timer"`; with `inject_sender` on, a sender line would sit in front of the pinned report text — `core/signoff.go:404`, `core/engine.go:16227`.
- `Message.Placeholder` reaches `processInteractiveEvents` through `interactiveState.placeholder`, set in the turn's own locked block right before the call and taken at its start; over a new parameter on `processInteractiveEvents`; it has 48 call sites, and nothing runs between set and take — `core/engine.go:4834`.
- Pending cards are claimed by taking the session's whole pending list under the lock and posting it in request order; over a claim per card; two cards released by one unlock would post in random order — `core/signoff.go:220`.
- `/ps` refusal tracks each report turn's reservation and counts a hold once that reservation is granted; over a flag set when the report turn is about to start; the hold begins at the grant while `signoff_exec` may still run for minutes — `core/signoff.go:357`.
- Idle close cancelled when the reservation is granted, by a goroutine waiting on it; over cancelling at the click only; a turn running at the click re-arms the timer just before it hands the lock over — `core/signoff.go:311`, `core/engine.go:3863`.
- `Session.CancelReservation` added; over leaving a reservation queued when cc-connect stops before the report turn; the next unlock would hand the lock to no one — `core/session.go:139`.
- `failed` report has no second line; the reason and stderr go to the log; over a cc-connect error text as the second line; the pinned second line is the command's `message` — `core/signoff_exec.go:40`.
- Empty stdout or `null` gives `failed`, trailing whitespace after the object is accepted; over requiring a trailing newline or none; `echo` adds one — `core/signoff_exec.go:92`.
- `SignoffValuePrefix` exported from core and read by Teams' intercept; over a Teams-local `"signoff:"`; one definition of the button value — `core/signoff.go:15`.
- A body line's text may hold a line break; over refusing it; the brief refuses only empty text and unknown styles, and a line break is still plain text — `core/signoff.go:52`.
- At a click the session is reserved before the card is redrawn; over redraw first; a message typed right after the click must already find the session reserved — `core/signoff.go:277`.
- Route tries engines in project-name order and takes the first whose agent the caller runs under; over map order; the same request always lands in the same engine — `core/api.go:778`.
- Route body capped at 1 MiB; over `/send`'s attachment-sized limit; the route carries no attachments — `core/api.go:773`.
- Teams signoff card posted without the "AI generated" label entity; over `aiCardActivity` as streaming cards use; every word on it is the gate's, not the agent's — `platform/teams/signoff.go:33`.
- Line spacing from the mockup: `name` default, `detail` none, `link` none but small after a `detail`, `heading` medium with a separator; the mark medium and bold; over one spacing for every line; the mockup groups a name with its details and links — `platform/teams/signoff.go:160`.
- Footer drops the name and its " · " when the clicker has no display name; over a line starting with " · " — `platform/teams/signoff.go:196`.
- Manual Teams check of the drawn card (U6 execution note) left for the review stop, and the U+2060 word joiner left unbuilt until that check shows Teams substitutes `{{DATE(` in a `TextRun`; over stopping the build; the pipeline has no Teams test channel and the joiner is conditional by the plan — plan U6.

## Review

Sources: `ce-code-review` and `tv:review` findings on `328f5a52`, `gh pr view 8`, `ch-content-bot` `origin/taras/gate-card-poc`.

- The rest of `signoff_exec`'s process group is killed as soon as the command has ended, whatever the outcome; over killing it only at the limit or a stop; a background child of a gate that answered `failed` could still publish, which KTD8 rules out. The proof-of-concept gate `exec`s one process and backgrounds nothing (`ch-content-bot` `origin/taras/gate-card-poc:docker/bin/content-gate-publish`), so nothing it needs is killed. A child that keeps stdout or stderr open still runs until it closes them, at most `WaitDelay` (2 s) — `core/signoff_exec.go:72`, `core/signoff_exec_unix.go:22`.
- `config.example.toml` says the limit gives `failed` and only a stop gives no report; over its earlier text, where the limit gave no report; the code reports `failed` at the limit, as KTD8 and the `## Plan review` line on the limit say. The `## Plan` line "A cc-connect stop or the limit … gives no report" means a stop only — `config.example.toml:311`.
- The route's 403 doc says the caller check needs an agent that reports its process id, which only Claude Code does; over leaving it unsaid; every other agent's callers get 403 — `core/signoff.go:176`, `config.example.toml:280`.
- Customer hosts, slugs and names in tests and `config.example.toml` replaced by `example.*` hosts and generic names; the user's ruling. Brainstorm and plan docs keep them.
- PR #8 is still open (`gh pr view 8`, 2026-11-19); `git merge-tree HEAD origin/taras/runas-agent-files` conflicts in `CHANGES.md` only, as KTD11 expects. Rebase onto #8 once it lands.
- Answered by this log, not changed: one user's card replacing another user's card in the same Teams conversation (`## Mechanism`, replacement keyed by thread); a failed card post never reaching the agent (plan R9, KTD5: log only).

## Open questions

### Line breaks and `{{DATE(` in a drawn `TextRun`

- Source: `tv:review` Question 1, with the manual U6 check still open. Needs a Teams test channel.
- Logged: "A body line's text may hold a line break; over refusing it; the brief refuses only empty text and unknown styles" (`## Build`). Finding: `validate` accepts `"a\nb"` in a `name` line, the title and the labels (`core/signoff.go:52`), and the Teams card puts it in one `TextRun` (`platform/teams/signoff.go:182`). If Teams breaks the line there, text from an agent's commit could draw as a second entry or link.
- Probe: post a card with a `name` line `a\nb` and one with `{{DATE(2026-11-19T10:00:00Z)}}`.
- Option A, keep: no change; costs a possible fake line on the card if Teams breaks the line.
- Option B, refuse `\r`/`\n` in every text in `SignoffRequest.validate`, as `parseSignoffExecOutput` does for `message`: a 400 for such text; changes the body format on the tripwire list, and the gate must keep line breaks out of names it reads from commits.
- Option C, draw `\r`/`\n` as a space in Teams: the request stays as is; the text is no longer shown exactly as sent.
- `{{DATE(` substituted: add the U+2060 joiner of KTD6; that is already decided.
- Recommended: run the probe; if `\n` breaks the line, B.

### Click conversation id in a channel thread

- Source: `tv:review` Question 2; plan "Assumptions" names it unverified. Needs a Teams test channel.
- The intercept drops a click whose `Conversation.ID` differs from the card's (`platform/teams/signoff.go:87`). If Teams sends a channel-thread click with another `;messageid=`, every channel click is dropped and the card stays open.
- Option A, keep the exact match: costs every channel click if Teams sends another id.
- Option B, compare the conversation id without `;messageid=`: costs the "only in the thread its card was posted in" rule on the tripwire list, since a click would count from any thread of the channel.
- Recommended: click a card in a channel thread; keep A unless the click is dropped, then decide B.

### Engine choice without the caller check, for the upstream PR

- Source: `tv:review` Question 3. Story owner's decision.
- Logged: "Engine found from `session_key` … keeps the brief's seven fields and the check separable" (`## Mechanism`). Finding: without the check, `handleSignoffSend` takes the first engine in name order with a live session for the key (`core/api.go:811-821`), so two projects whose bots share a chat cannot be told apart; the check is not separable that way.
- Option A, keep the caller check as what picks the engine, upstream too: no change; the route works on Linux only.
- Option B, add an optional `project` field like `/send`: an eighth field, on the tripwire list; the gate does not need it.
- Option C, ship upstream without the check: no change here; two projects sharing a chat get the wrong engine.
- Recommended: A. The content bot runs one project, so nothing is needed now.
