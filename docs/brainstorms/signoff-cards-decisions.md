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
- Click intercept after the serviceURL checks and before `allow_from`; over after `allow_from` (first draft, reversed at plan review); the brief says anyone in the thread may click — `platform/teams/webhook.go:123-153`.
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

## Build

## Open questions

### Signoff click and `allow_from`

- **Question:** does a click from someone in the thread who is not on the Teams `allow_from` list count?
- **Why it is open:** the brief says "Anyone in the thread may click" and puts where the click is handled on the tripwire list; it does not mention `allow_from`. The content bot requires `allow_from` in every deployment ("Anyone else gets no answer", `ch-content-bot` `origin/master:README.md:50`, `CHANGES.md:7-11`), and prod sets it to two Entra ids (`op-argo-apps` `origin/master:prod/radio/kustomization.yml:105`, `prod/regiotv/kustomization.yml:106`; stg is `*`). The bot runs in channels (`channel_files_enabled = true`), where every team member sees the card. ch#2022's AC is "goes live only after **the editor** confirms the summary". The plan's first draft put the intercept after `allow_from`; it was reversed on the brief's sentence.
- **Blocks:** KTD7, R14, U6 step 3 and its `allow_from` test, U7's "who may click" in `docs/teams.md`; and the gate in ch-content-bot (WP5), which must know whether it has to check `clicked_by.id` itself.
- **Option A — before `allow_from`, as planned:** any member of the channel can approve a publication from a bot that would not take an order from them. Closing that falls to the gate, which would re-check `clicked_by.id` against `TEAMS_ALLOW_FROM` — the same list kept in two places.
- **Option B — consume every `signoff:` value before `allow_from`, call `onClick` only when the clicker passes `core.AllowList(p.cfg.allowFrom, userID(a))`; otherwise log and leave the card open:** one call in the intercept; a team member outside the list cannot approve — the same people the bot already ignores. "Anyone in the thread" then reads "anyone the bot answers in the thread", which is still not only the requester. No stale value can become a permission message, since consumption still comes first.
- **Recommended:** B. It keeps the deployment's one list of who may act through the bot, and costs one line plus one test.

### Outcome report text

- **Question:** may the report carry the gate's `request_id` — `Signoff <request_id>: <outcome>`, `replaced` adding ` by <newer request_id>`, the command's `message` on a second line — or only fixed words, the outcome and the `message`?
- **Why it is open:** the tripwire list pins the report to "fixed text, the outcome and the command's `message`, never the nonce"; `request_id` is none of those. The bot's skill in ch-content-bot (WP5, "The bot tells the editor each refusal and each card's outcome") is built against the exact text, and the brief's list of what this unit pins for the gate (style names, `clicked_by` names, time limit) does not name it.
- **Blocks:** KTD10, U5's report-text tests, U7's documentation of the report, and the WP5 skill text that reads it.
- **Option A — with the `request_id`, as planned:** a gate-chosen string (non-empty, no `:`) enters the agent's prompt; the gate is root and the only caller, and the id is not secret (it is in the button value). The agent can tell two reports in one session apart and, on `replaced`, which card is still open.
- **Option B — `Signoff: <outcome>` and the `message` line only:** exactly the tripwire's contents. With two cards in one turn the agent gets `replaced` then, later, another outcome, and must rely on order; the gate can still put its own id into `message` for `published`, `refused` and `failed`, but `cancelled` and `replaced` carry no `message`.
- **Recommended:** A, recorded as a pinned contract the gate reads, beside the style names and `clicked_by`.
