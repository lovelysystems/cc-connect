---
title: Teams Formatted Message Context - Plan
type: feat
date: 2026-11-04
topic: teams-formatted-message-context
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-brainstorm
execution: code
---

# Teams Formatted Message Context - Plan

## Goal Capsule

- **Objective:** Editors who paste links and article drafts into the content bot's Teams channel
  get articles that keep the links and the draft's meaningful structure, without Teams styling.
- **Means:** the Teams connector adds the message's HTML as labelled context for the agent
  (KTD1–KTD4).
- **Product authority:** this plan. The Product Contract spans the Teams connector in cc-connect
  (`downstream` branch) and the content-authoring rich-text rule in ch-content-bot; this plan's
  implementation units cover the cc-connect part (R1–R6) only.
- **Execution profile:** one PR in lovelysystems/cc-connect against `downstream`. R7–R9 ship
  separately in ch-content-bot.
- **Stop conditions:** stop and report if the engine turns out to match commands or permission
  replies against the merged text, which would break R2.
- **Open blockers:** none.
- **Product Contract preservation:** restructured, no scope change — the "which tags count as
  formatting" question moved from Outstanding Questions to KTD2; R1 and its Key Decision now say
  "link target or formatting the plain text lacks", the wording agreed in the brainstorm. Two
  non-goals found during planning were added to Scope Boundaries.

---

## Product Contract

### Summary

The Teams connector keeps handing the agent the plain message text, and adds the Teams HTML of
the message as labelled context when that HTML carries a link or formatting. ch-content-bot treats
formatting that comes with a chat message as a hint for structure, not as markup to copy.

### Problem Frame

Editors post from Teams desktop or web into a channel, pasting article drafts and links for the
content bot. Teams turns a pasted URL into a link labelled with the page title, and the connector
reads only the activity's plain `text`, which keeps the label and drops the target. The PO reported
that the agent gets the page title but not the URL. Pasted drafts lose their headings, lists and
emphasis in the same way; nobody has complained about that yet, but it is the same loss.

Teams already sends the full message as HTML in a `text/html` attachment on every channel message
from desktop and web, including unformatted ones. The connector ignores it.

Pasted text often looks bad in Teams: Word styling, fonts and stray breaks come along. So the
formatting is useful as a hint about structure, but copying it exactly would put that mess into
articles. ch-content-bot's rich-text rule today says formatting "typed in chat" is carried across
as markdown; it has never applied to Teams, because no formatting arrived.

### Key Decisions

- **The plain text stays the message; the HTML travels next to it as context.** Commands,
  permission replies and aliases keep matching the plain text, and the agent sees the formatting
  as source material, not finished markup. Governs R1, R2. (session-settled: user-approved —
  chosen over converting the HTML to Markdown as the message, links only, and raw HTML: the
  context form gives the agent formatting as a hint without making it look final.)
- **The HTML context is added only when it carries a link target or formatting the plain text
  lacks.** Nearly every channel
  message has HTML, so attaching it always would duplicate every short reply. Governs R1, R3.
  (session-settled: user-approved — chosen over always attaching it.)
- **cc-connect does not clean the HTML.** Size and weight can mark a heading in a pasted draft,
  so styling is a hint too; the agent decides what it means. Governs R4. (session-settled:
  user-directed — chosen over keeping only tags and link targets: the styling carries structure.)
- **The agent's rule decides what the formatting means, and it is generic.** ch-content-bot may
  run on other chat platforms, so the rule covers formatting from any chat message and the label
  names no platform. Governs R5, R7, R9. (session-settled: user-directed — chosen over a rule that
  names the Teams label.)
- **Formatting from chat is used, not copied exactly.** Governs R7. (session-settled:
  user-directed — chosen over treating it as something not to copy at all.)
- **No on/off switch.** ch-content-bot is the only Teams deployment, so a config key would have no
  caller.

### Requirements

**Teams connector (cc-connect)**

- R1. When a Teams message carries an HTML version that contains a link target or formatting the
  plain text lacks, the agent receives that HTML as labelled context next to the plain message
  text.
- R2. The plain message text stays the message: commands, permission replies, aliases and card
  actions behave as today.
- R3. Mentions are removed from the HTML context, as they are from the plain text, and a mention
  alone does not count as formatting.
- R4. Apart from mention removal, the HTML context is the message as Teams sent it, styling
  included.
- R5. The label says the block is the formatted original of the message and names no platform.
- R6. A message without an HTML version, or with an empty one, reaches the agent exactly as today.

**Content authoring (ch-content-bot)**

- R7. Formatting that comes with a chat message, in whatever form it arrives (Markdown in the
  text, or a formatted copy of the message as context), is a hint for structure: links, headings,
  lists and emphasis on words are kept, size or bold that marks a heading is read as a heading,
  fonts and colours are ignored, and when unsure the article's own conventions win.
- R8. Files and documents keep carrying their formatting into rich-text fields as today.
- R9. The rule names no chat platform and no exact label text.

### Acceptance Examples

- AE1. Pasted link. **Covers R1.**
  - **Given** an editor pastes `https://example.com/x` and Teams shows it as "Example Page Title"
  - **When** the message reaches the agent
  - **Then** the agent's input contains `https://example.com/x`
- AE2. Plain reply. **Covers R1, R3.**
  - **Given** an editor writes "@Content Bot ok, publish it" with no formatting
  - **Then** the agent receives only "ok, publish it", with no HTML context
- AE3. Command. **Covers R2.**
  - **Given** an editor sends `/new` with the bot mentioned
  - **Then** the command runs as today
- AE4. Pasted Word draft. **Covers R7.**
  - **Given** a draft with two headings, a bulleted list, a bold word and a custom font
  - **When** the agent fills the article body
  - **Then** the body has the headings, the list and the bold word, and no font information
- AE5. iOS single line. **Covers R6.**
  - **Given** a single-line message from the iOS client, which arrives without an HTML version
  - **Then** the agent receives the plain text as today

### Scope Boundaries

- Messages without an HTML version: a link pasted on its own line from iOS still loses its URL. A
  Microsoft Graph read as fallback is deferred until this shows up in real use.
- Cleaning or shrinking the HTML: deferred until a captured Word paste shows the size matters.
- Converting the HTML to Markdown in cc-connect.
- Other chat platforms in cc-connect.
- Contributing this to upstream chenhg5/cc-connect.
- Answers to the agent's questions: a link pasted as the answer to an agent question or a
  permission prompt still loses its URL, because the engine resolves those answers from the plain
  text before the context is used (`core/engine.go`, `handlePendingPermission`). Considered and not
  built: answers are short choices today; a real need for links there would change the call.
- Reading the HTML from the Graph copy of a channel message: when `channel_files` is on, the
  connector already reads every engaged channel message via Graph, and that copy carries the HTML
  even for iOS. Considered and not built: editors use desktop and web; real iOS link losses would
  change the call.

### Dependencies / Assumptions

- Teams sends a `text/html` attachment on channel messages from desktop and web, including
  unformatted ones. Recorded in the channel-file spike and Microsoft's sample payload; editors use
  exactly these clients.
- The mention appears in the HTML as a `span` with
  `itemtype="http://schema.skype.com/Mention"` (Microsoft samples). Unconfirmed in our tenant.

### Outstanding Questions

**Deferred to Implementation**

- Capture real activities from our tenant after deploying to stg: a mention, a pasted
  title-converted link, a pasted Word draft, and a plain typed message. They confirm the mention
  markup, show how large a Word paste is, and show whether Teams sets `font-size` or `font-weight`
  on plain typed messages, which would make KTD2's style rule fire on everything.

### Sources / Research

- `platform/teams/activity.go:148` — `cleanText` reads only `activity.Text` and removes every
  mention entity.
- `platform/teams/webhook.go:103` — dispatch uses `cleanText` as the message content.
- `core/engine.go:2869-2884` — `ExtraContent` is put in front of the message for the agent;
  commands and permission replies are matched against the plain content (about lines 2974-2983).
- `platform/telegram/telegram.go:585-601` — Telegram adds reply quotes and location as
  `ExtraContent` and rewrites dropped link targets inline as `[label](url)`.
- `docs/plans/2026-07-08-001-feat-teams-channel-file-reading-plan.md:47` — channel activities
  carry a `text/html` attachment.
- ch-content-bot `plugin/skills/content-authoring/references/rich-text.md:10-11` — the current
  "typed in chat" rule.
- [Microsoft: incoming message sample](https://learn.microsoft.com/en-us/microsoftteams/platform/bots/build-conversational-capability)
  — `text` is plain, the HTML is in a `text/html` attachment.
- [Microsoft Q&A: formatted user input](https://learn.microsoft.com/en-us/answers/questions/1321647/teams-bot-dev-access-the-original-formatted-user-i)
  — iOS omits the HTML for single-line messages.
- [lovelysystems/ch#2214](https://github.com/lovelysystems/ch/issues/2214) — the task for the
  link problem.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Walk the HTML with the `golang.org/x/net/html` tokenizer and write each kept token's
  raw bytes.** Mention elements (a `span` whose `itemtype` ends in `schema.skype.com/Mention`, and
  `at`) are skipped with everything inside them; every other token is copied byte for byte, so
  quoting, entities and styling stay as Teams sent them. Re-serialising parsed nodes would change
  them. Each token's raw bytes are copied right after `Next()`, before `TagName`, `TagAttr`,
  `Text` or `Token` is called, because those calls lowercase and unescape the tokenizer's buffer
  in place; a skipped mention discards its copy. `golang.org/x/net` becomes a direct dependency; it is already in `go.mod` as indirect with
  hashes in `go.sum`. Governs R3, R4.
- KTD2. **The formatting check runs on the tokens kept after mention removal, against an explicit
  list.** It counts:
  - `a` with an `href` whose entity-decoded, trimmed text differs from the `href`;
  - `b`, `strong`, `i`, `em`, `u`, `s`, `strike`, `del`, `h1`–`h6`, `ul`, `ol`, `li`, `pre`,
    `code`, `table`;
  - `blockquote`, unless its `itemtype` marks a Teams reply quote (ends in `/Reply`); tags inside
    a reply quote do not count either, though their bytes are still copied;
  - any element whose `style` sets `font-size` or `font-weight`.

  Everything else does not count: `p`, `div`, `br`, `span` without those styles, `img`, `emoji`,
  `attachment`, entities such as `&nbsp;`. A typed URL Teams auto-links already appears in the
  plain text, so it adds nothing. Governs R1, R3.
- KTD3. **The context block has an opening and a closing marker and joins existing notes.** It
  reads `[Formatted original of this message (HTML)]`, a newline, the HTML, a newline,
  `[End of formatted original]`. The engine puts `ExtraContent` in front of the text with a
  single newline, so without the closing marker the agent cannot tell where the copy ends. The
  attachment notes from dispatch (`attachmentsNotCheckedNote`, `attachmentDropNote`) come first;
  the block is appended after a blank line. Governs R1, R5.
- KTD4. **The context is built only for a non-card message whose plain text is non-empty, and it
  never affects whether a message is dispatched.** The webhook's empty-message gate stays on plain
  text and processable attachments, because the engine accepts a turn that carries only
  `ExtraContent`. Governs R2, R6.

### Assumptions

- Teams renders mentions in the HTML as the `span` or `at` element KTD1 names. A different form in
  our tenant would leave the mention in the context and make KTD2 count nothing extra, since
  neither element is on its list.
- Teams does not set `font-size` or `font-weight` on plain typed messages. If it does, KTD2's
  style rule needs narrowing after the stg capture.

---

## Implementation Units

### U1. Build the formatted context from the HTML attachment

- **Goal:** a function in the Teams package that, given an inbound activity, returns the context
  block for the agent, or an empty string.
- **Requirements:** R1, R3, R4, R5; KTD1, KTD2, KTD3.
- **Dependencies:** none.
- **Files:**
  - `platform/teams/html_context.go` (new)
  - `platform/teams/html_context_test.go` (new) — unit seam on raw activity JSON; catches
    parsing, mention removal and the formatting check, misses dispatch wiring.
  - `go.mod` (`golang.org/x/net` moves to the direct block)
- **Approach:**
  1. Take the first attachment whose `contentType` equals `text/html` (case-insensitive) and
     decode its `content` JSON string. Missing, empty or non-string content returns "".
  2. Tokenize once: skip mention elements with their contents, copy other tokens' raw bytes, and
     set the formatting flag per KTD2.
  3. No flag returns "". Otherwise return the block per KTD3, with the HTML trimmed of
     surrounding whitespace.
- **Patterns to follow:** `inboundAttachment` and `isFileDownload`/`downloadInfo` in
  `platform/teams/activity.go` for attachment typing and lazy JSON decoding; raw-JSON
  `parseActivity` tests in `platform/teams/activity_test.go`.
- **Test scenarios:**
  - Covers AE1. A pasted, title-converted link
    `<p><a href="https://example.com/x">Example Page Title</a></p>` returns the block with the
    `href` intact.
  - Microsoft's sample `<div><div>Hello. Sending <strong>bold</strong>-<em>italic</em> rich
    text.</div></div>` returns the block.
  - Covers AE2. `<p><span itemtype="http://schema.skype.com/Mention" itemscope itemid="0">Content
    Bot</span> ok, publish it</p>` returns "".
  - The same message with `<at>Content Bot</at>` returns "".
  - A mention of another person plus `<b>today</b>`: the block keeps `<b>today</b>` and contains
    no mention text.
  - A typed URL `<a href="https://example.com/a?b=1&amp;c=2">https://example.com/a?b=1&amp;c=2</a>`
    returns "".
  - `<p style="font-size:18pt">Heading</p>` returns the block; `<span style="color:red">x</span>`
    alone returns "".
  - An emoji `<img>`, an `<attachment id="1">` and a reply quote
    `<blockquote itemtype="http://schema.skype.com/Reply"><strong itemprop="mri">Name</strong><p
    itemprop="preview">text</p></blockquote>` alone each return "".
  - A title-converted link whose `href` contains an entity,
    `<a href="https://example.com/a?b=1&amp;c=2">Page Title</a>`, appears in the block byte for
    byte.
  - A plain `<blockquote>` returns the block.
  - Raw bytes survive: single-quoted attributes, `&nbsp;` and inline `style` appear in the block
    exactly as in the input, minus the mention.
  - No `text/html` attachment, empty content, or content that is not a JSON string returns "".
  - `contentType` `Text/HTML` is accepted.
- **Verification:** all scenarios pass; the block for AE1 starts with the opening marker and ends
  with the closing marker.

### U2. Attach the context in dispatch

- **Goal:** messages dispatched to the engine carry the block in `ExtraContent` when U1 returns one.
- **Requirements:** R1, R2, R6; KTD3, KTD4.
- **Dependencies:** U1.
- **Files:**
  - `platform/teams/webhook.go`
  - `platform/teams/session_test.go` — dispatch seam with the `collector()` handler; catches
    wiring, note joining and the card-action and empty-text paths, misses the engine's merge
    (covered by the engine's own tests).
- **Approach:**
  1. In the non-card branch of `dispatch`, after the attachment notes are set, build the block
     when the plain content is non-empty (KTD4).
  2. Append it to `msg.ExtraContent` per KTD3, leaving the existing notes first.
  3. Leave the empty-message gate and the card-action branch unchanged.
- **Patterns to follow:** existing `ExtraContent` assertions in `platform/teams/session_test.go`
  (the attachment-note tests) and the `personalActivity`, `messageActivity`,
  `channelFileActivity`, `teamsPlatform` and `collector` helpers there.
- **Test scenarios:**
  - Covers AE1. A channel message with a title-converted link: `Content` is the plain text and
    `ExtraContent` is the block.
  - Covers AE2. An @mention plus "ok, publish it" with plain HTML: `ExtraContent` is empty.
  - Covers AE3. `/new` with a mention and formatted HTML: `Content` is exactly `/new`.
  - Covers AE5. A message with no `text/html` attachment: `ExtraContent` is empty and `Content`
    is unchanged.
  - A card action whose activity carries formatted HTML: no block.
  - A 1:1 message whose image download fails and whose HTML has a link: `ExtraContent` holds the
    drop note, a blank line, then the block.
  - A channel message with empty plain text and formatted HTML (channel files on): no block, and
    whether it dispatches is unchanged from today.
- **Verification:** all scenarios pass; existing Teams tests still pass unchanged.

### U3. Document the behaviour

- **Goal:** operators and maintainers can see that the agent receives the formatted message.
- **Requirements:** R1, R5.
- **Dependencies:** U2.
- **Files:**
  - `docs/teams.md` — one bullet in "How it works".
  - `CHANGES.md` — one entry under `## v1.5.0` → `### Unreleased`, appended after the existing
    entries, marked `(upstream: pending)`.
- **Approach:** state what the agent receives and when, without explaining why.
- **Test expectation:** none -- documentation only.
- **Verification:** the bullet and the entry exist and match the shipped label text.

---

## Verification Contract

| Check | Command | Proves |
|---|---|---|
| Build | `go build ./...` | the dependency move and new file compile |
| Vet | `go vet ./platform/teams/` | no vet findings in the changed package |
| Unit and dispatch tests | `go test ./platform/teams/` | U1 and U2 scenarios |
| Race | `go test -race ./platform/teams/` | dispatch stays race-free |
| Engine | `go test ./core/` | command and permission matching unchanged |
| Core stays agnostic | `grep -n teams core/*.go` shows no new hits | no platform name added to core |

---

## Definition of Done

- U1–U3 are complete and every scenario above passes.
- `go build ./...` and `go test ./...` pass.
- No code is left from abandoned approaches, and no comment in the diff explains what was not
  done.
- The PR targets `downstream` in lovelysystems/cc-connect.
