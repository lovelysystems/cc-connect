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

## Build
