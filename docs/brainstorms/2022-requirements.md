# Unit brief: signoff-cards

cc-connect posts a signoff card — a summary with Approve and Cancel — into a thread on request of
a root-only program, and only a person's click on Approve runs that program's configured command,
so an agent can never sign off its own change.

Story: lovelysystems/ch#2022
Workpackage: ch#2022 WP1
Task: lovelysystems/ch#2320

Base branch: `downstream`. The pull request targets `downstream`, never `main`: `main` only
mirrors upstream (`DOWNSTREAM.md`, "Branches").

`docs/plans/` on this branch holds upstream's own plan files. None of them is this unit's plan;
planning for this unit has not run yet.

## Acceptance criteria this unit serves

ch#2022 and ch#2023 are planned as a pair. This unit serves:

- ch#2022: "The publication goes live only after the editor confirms the summary"
- ch#2023: "The limit also holds when the bot itself is prompt-injected"

How it serves them: the content bot runs its agent as the user `claude`, and cc-connect runs as
root in the same pod. A root-only program in the bot's image, the gate, will be the only thing
that can push the live content. It builds a summary of the exact commit it would push and asks
cc-connect to show it in the editor's thread. Only an editor's click on Approve makes cc-connect
run the gate's publish command. The agent can ask for a card, but nothing it types, writes or
runs can press the button. The gate itself is built in another repo; this unit builds the card,
the click and what the agent hears afterwards.

## This unit's slice

### The route

`POST /signoff/send` on cc-connect's root-only API socket (`core/api.go:92-105` lists the
existing routes). It posts a signoff card into the thread named by `session_key` and answers
`posted` or a refusal with its reason.

The request, a JSON body:

- `session_key` — the thread the card goes to, taken the way `/send` and `/cron/add` take it.
- `request_id` — the gate's id for this request; travels in the button value and back on the
  click.
- `nonce` — random, from the gate; same path as `request_id`, nowhere else.
- `title` — the card header, e.g. "5 changes".
- `body` — a list of lines, each plain text with a style from a small fixed set: a heading, a
  name, a detail line, a link. The style names are this unit's to choose.
- `confirm_label` — the confirm button's text, e.g. "Approve" or "Freigeben".
- `cancel_label` — the Cancel button's text, e.g. "Cancel" or "Abbrechen".

The route refuses:

- a caller whose process parents do not reach that session's agent process — a guard against a
  card landing in the wrong thread by mistake, not a wall between agents (they all run as one
  user);
- a project without `run_as_user`;
- a platform that does not implement the signoff interface;
- a missing field, an empty `body`, or a line with a style outside the set.

The format has no way to express a button or an input, and every text in it is plain text: shown
as written, never read as markdown, HTML or any card format.

### The card

- Each `body` line becomes a new element of `core.Card` (`core/card.go:23`): plain text with a
  style. cc-connect builds the signoff card from the header, those elements and the button row.
- Drawing: a new optional platform interface with two jobs — post a signoff card, and redraw it
  at the click or when a newer card replaces it. Teams implements it. Teams does not implement
  `CardSender` (`core/interfaces.go:351`), so `/help`, `/model` and every other card stay text on
  Teams. Another platform shows signoff cards only once it implements the interface; the text
  fallback (`Card.RenderText`) never carries one.
- cc-connect adds no words of its own to a signoff card. Every word on it comes from the request.
- The card is posted the moment the agent's final reply of the current turn goes out, directly
  under it — in the turn code every turn runs through. The route answers `posted` before the
  platform has the card; a post that then fails shows in cc-connect's log only.
- The Approve button value is `signoff:<request_id>:<nonce>`; Cancel carries a second value bound
  to the same request and nonce.

### The click

- A click counts only as a button press on that card, in the thread it was posted in, once, with
  the matching nonce. It is handled in the Teams handler before any message is built. Open cards
  are kept in memory only, never written to disk.
- No typed word resolves a signoff card: not "approve", "ok", "allow all", "确认" or any other
  permission keyword (`core/engine.go:3653-3674`), and not an "Allow all" already granted in the
  session.
- The click never reaches the agent as a message. The agent hears about it only through the
  outcome report, after the command has answered.
- Anyone in the thread may click.
- At the click, before anything runs, the card is redrawn once: its buttons go, and a small line
  under a divider records the click — a green ✓ (Approve) or red ✕ (Cancel) in a narrow column
  of its own, then the clicker's name and the date and time, which Teams shows in each viewer's
  locale and timezone: "Lovely Editor · 17.11.2026, 10:42". No words. The card never shows the
  outcome, and cc-connect posts no message of its own.
- On Approve, cc-connect runs the project's new config key `signoff_exec`, a shell command, as
  `exec` is elsewhere in cc-connect's config. On stdin, one JSON object: `request_id`, `nonce`,
  and `clicked_by` with the clicker's platform user id (on Teams the Entra object id) and display
  name. On stdout, one JSON object: `outcome` — `published` or `refused` — and `message`, one
  line. A command that exits non-zero, prints anything else, or runs past a time limit is
  outcome `failed`.
- On Cancel, the card is void for good. `signoff_exec` is not run.
- A newer card in a thread voids every older open card in that thread. When the newer card goes
  out, each older one is redrawn once: its buttons go, and the line under the divider shows a
  grey ↻ and the time — no name, no words. A click on a voided card does nothing.

### The session hold and the outcome report

- From the click until the outcome turn ends, the thread's session is held. A "Typing…"
  placeholder appears under the card at the click, and the outcome turn writes into it. A message
  typed meanwhile is queued with cc-connect's queue notice and answered after the outcome.
- After every card outcome — published, refused, cancelled, replaced by a newer card, failed —
  cc-connect starts a turn in the card's session with a report: fixed text naming the outcome,
  and the command's `message` when there is one. Never the nonce. It starts the turn the way a
  timer starts its turn (`/timer/add`, `core/engine.go:1664`); after a click, on the session the
  click holds. Timer turns give up on a busy session today, so this is a new path beside them.

### Cases, each with its own test

Route:

- A request from a process under the session's agent posts the card and answers `posted`.
- A request from a process outside every agent, or under another session's agent, naming this
  session, is refused.
- A project without `run_as_user` is refused.
- A platform without the signoff interface is refused.
- A missing field, an empty `body`, or a line with an unknown style is refused.
- Body text holding markdown, HTML or card JSON shows as written.

Posting:

- A card requested during a turn appears directly under that turn's final reply.

Click:

- Approve with the right nonce: buttons go, ✓ line with name and time, `signoff_exec` runs with
  `request_id`, `nonce` and `clicked_by` on stdin, and its `outcome` and `message` reach the
  outcome report.
- A second click on the same card runs nothing.
- A click carrying a wrong or reused nonce runs nothing.
- A button value replayed from another thread runs nothing.
- "approve", "ok", "allow all" and the other permission keywords typed in the thread, and an
  "Allow all" granted earlier in the session, leave the card open and run nothing.
- The click never reaches the agent as a message.
- `signoff_exec` exiting non-zero, printing something other than the JSON object, or running past
  the time limit gives outcome `failed`.
- Cancel: ✕ line with name and time, card void, `signoff_exec` not run, outcome `cancelled`.
- A newer card in the same thread: the older one shows ↻ and the time, has no buttons, a click on
  it runs nothing, outcome `replaced`.
- A newer card in another thread leaves this thread's card open.

Session hold and outcome report:

- At the click, a "Typing…" placeholder appears under the card and the outcome turn writes into
  it.
- A message typed between the click and the end of the outcome turn is queued with the queue
  notice and answered after the outcome.
- The outcome turn is never dropped because the session is busy.
- The outcome report never carries the nonce.

Teams:

- Teams does not implement `CardSender`: `/help` and the other cards stay as they are on Teams.

## The decisions this brief prefers

- The signoff card is its own kind, apart from the permission card. The permission card prints
  the agent's own command, and a typed "ok" or "Allow all" approves it.
- The names `/signoff/send`, `signoff_exec` and `signoff:` follow upstream's words. `send` posts
  into a session (`/send`, `/relay/send`); `add` makes a stored, listed item (`/cron/add`). Upstream
  already uses "approve" and "confirm" for permission answers. In the config, `exec` is a shell
  command and `command` a slash command.
- The body is a list of styled plain lines, platform-neutral, never a platform's card format: the
  gate runs wherever cc-connect bridges, and Slack or Telegram would each need a second builder.
- A new `core.Card` element, over a line format of our own beside `core.Card` (a second card
  model). The markdown element needs escaping, which the plain-text rule forbids; the note draws
  as a small footnote; list items and selects carry buttons.
- A narrow optional interface on Teams, over Teams implementing `CardSender`: the engine checks
  `supportsCards` in 29 places (`core/engine.go:11932`), and implementing it moves every other
  card on Teams from text to cards, whose buttons then need in-place navigation. One optional
  interface per capability is how cc-connect extends platforms (`InlineButtonSender`,
  `CardRefresher`, `StreamingCardPlatform`).
- No text-plus-buttons fallback through `InlineButtonSender` for other platforms. Platforms turn
  button presses into messages (Telegram: `platform/telegram/telegram.go:836-860`), so each would
  need its own signoff click handling anyway — that is implementing the interface.
- Posted with the final reply, over posting at the request (it lands under an empty "Typing…"
  placeholder, the reply filling in above it) and over posting after the turn (about 2 s late).
- The card changes once, at the click, over updating it at each step and posting a separate status
  message — both read as noise in the proof of concept.
- The button press hides Teams' "Your response was sent to the app" banner: `Action.Submit` with
  the hide flag on the button; `Action.Execute` does not hide it (tested in Teams).
- The route takes `session_key` as cc-connect's existing routes do, so it can be offered upstream
  once upstream has Teams; the caller check stays in our fork. Keep the caller check separable
  from the route for that reason.
- Reference: branch `taras/gate-card-poc` on origin built this card as a proof of concept and was
  run against a Teams bot (`core/gate.go`, `platform/teams/gate.go`, `core/peercred_linux.go`).
  Read it for the Teams card's look and the quirks it found. It is not the design: it says "gate"
  where this says "signoff", it has an `eyebrow` field that was dropped, it runs the command from
  the Teams package, and its body is not the styled-line format.

## Mockups

This unit runs under a mockup gate. Planning builds a throwaway HTML mockup of the signoff card in
Teams, in a gitignored path, in three states: open, after the click (✓ and ✕), and replaced (↻).
The run stops after the plan review with their paths; implementation starts only on approval.

- Look: the proof of concept's card on `taras/gate-card-poc` — large title, filled confirm button,
  the click line in a small grey footer under a divider, the mark in its own narrow column.
- Words: only those the gate sends. Use this card, in English, and the German labels:

  ```
  5 changes
  ENTRIES
  winterklaenge-am-see-2027
    www.radio24.ch · www.radio32.ch
    🔗 https://tickets.example.ch/winterklaenge
    🔗 https://www.seebad-example.ch/anfahrt
  herbstfest-2026
    www.radio24.ch
  test-event-november-2026
    www.radio32.ch
  LISTS
  artikel-win
    www.radio32.ch
  events-uebersicht
    www.radio24.ch
  [Approve] [Cancel]
  ```

  German: "Änderung", "Änderungen", "Einträge", "Listen", "Freigeben", "Abbrechen". Never words
  from the story or the code ("signoff", "gate", "request").

## How this repo wants changes made

- One pull request into `downstream`, squash-merged.
- Written PR-ready for upstream: upstream conventions, tests, no downstream-only glue
  (`DOWNSTREAM.md`, "Goal: dissolution"). `core/` stays free of platform names (`CLAUDE.md`).
- `CHANGES.md` entries under `## v1.5.0` → `### Unreleased` → `#### Feature`, in the style
  `- **scope**: description (upstream: pending)`. Never edit `CHANGELOG.md`; upstream owns it.
- The route and `signoff_exec` documented where the repo documents its socket routes and config
  keys (`config.example.toml`), and the card in `docs/teams.md`.
- `go test ./...` passes before each commit (`CLAUDE.md`).

## Cross-cutting constraints

- The gate, in ch-content-bot, is the only caller of the route and the only `signoff_exec`. It is
  built later against this unit's route, fields, stdin and stdout as merged. Whatever this unit
  pins beyond the names above — the style names, the inner names of `clicked_by`, the time limit —
  the gate reads from this repo, so log each.
- The route's refusal of a project without `run_as_user` relies on another unit in this repo
  (open as PR #8, `taras/runas-agent-files`), which makes cc-connect do its work on the agent's
  files as the agent's user. It merges first; rebase onto it.
- Release order: this cc-connect change is released first, then the bot's base image raises its
  cc-connect pin, then the gate can merge.

## What to leave alone

- **Hooks, the usage readout and attachment writes under `run_as_user`:** PR #8 builds them. It
  also touches `core/message.go`, `agent/claudecode/session.go` and `CHANGES.md`; stay out of its
  hunks.
- **The gate:** the summary, which pushes need a card, the count, the limit, the push itself —
  all in ch-content-bot.
- **Teams implementing `CardSender`**, and the signoff interface on any platform but Teams.
- **A text-plus-buttons fallback for other platforms.**
- **A CLI command for the route** (`cc-connect signoff send`): nothing calls it in this change.
- **An `eyebrow` field** or any field beyond the seven above.
- **The upstream pull request itself:** posted by hand later.
- **`main`:** no commit lands there.

## Capture instruction

> Whenever you make a choice this brief and the unit plan do not dictate, and a reasonable
> implementer could have gone the other way, append one line to
> `docs/brainstorms/signoff-cards-decisions.md`: what you chose, the alternative you rejected, one
> clause of why, and `file:line`. Write it as you go, never reconstructed at the end — a decision
> looks inevitable once the code around it exists, which is exactly how it escapes notice.

This binds both planning and implementation. Planning writes what it pinned that this brief did
not under `## Plan`; implementation writes what it decided while building under `## Build`. One
file, one list.

## When to ask

> Ask when being wrong is expensive beyond this unit; log and continue when it is contained within
> it. Concretely, at the moment a choice appears: if what binds resolves it, it is not a decision —
> do it. If either option would contradict what binds, stop: that is a requirements defect, not a
> choice. If choosing wrong would cost work outside this unit — it crosses a unit boundary, touches
> anything on the tripwire list, or becomes visible outside the system at release — stop: write the
> question under `## Open questions` in `docs/brainstorms/signoff-cards-decisions.md`, commit and
> push, and end the run. Otherwise decide, log one line, and carry on.
>
> What binds is the acceptance criteria, the outcomes the requirements state, the contracts this
> brief names, and the tripwire list. Where this brief or the requirements name a mechanism —
> which component does the work, which fields, how many, validated how — that is a preference, not
> a settled decision. When the code supports a better shape for the same outcome — the way the repo
> already does the job, or a simpler one — build that, and log it in the decisions file with the
> mechanism it replaced and the `file:line` that supports it. Dislike of a preference is not
> evidence; the code is.

"The requirements" here means the acceptance criteria above, the settled decisions this brief
carries, and the story's PO answers.

### Tripwire list

The entries that concern cc-connect:

- The route `POST /signoff/send` and its fields: `session_key`, `request_id`, `nonce`, `title`,
  `body`, `confirm_label`, `cancel_label`; and the check that the caller belongs to `session_key`.
- The `body` format: plain-text lines with a style from a fixed set, nothing that clicks, nothing
  in any platform's card format. The new `core.Card` element they become.
- Content from the request read as markup anywhere on its way to the card.
- Teams implementing `CardSender`, or any other card drawn through the signoff interface.
- The config key `signoff_exec`.
- The button value `signoff:<request_id>:<nonce>`, and any path by which typed text or "Allow all"
  resolves it.
- Where a signoff click is handled: in the Teams handler before any message is built, only in the
  thread its card was posted in, kept in memory, never written to disk. The click never reaches
  the agent; only the outcome report does, after the command has answered.
- What `signoff_exec` gets on stdin (`request_id`, `nonce`, `clicked_by`) and answers on stdout
  (`outcome`, `message`).
- The outcome report cc-connect hands the agent: fixed text, the outcome and the command's
  `message`, never the nonce.
- An outcome turn dropped because the session is busy. The click holds the session until the
  outcome turn ends.
- Anything cc-connect runs, reads or writes as root on a path the agent's user can write.
- Anything that lets the agent's user run the gate's publish command or reach its directories.
