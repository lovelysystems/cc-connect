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
- **Product authority:** this plan. It spans the Teams connector in cc-connect (`downstream`
  branch) and the content-authoring rich-text rule in ch-content-bot.
- **Open blockers:** none. The payload captures under Outstanding Questions happen during
  planning.

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
- **The HTML context is added only when it carries a link or formatting.** Nearly every channel
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

- R1. When a Teams message carries an HTML version that contains a link or formatting, the agent
  receives that HTML as labelled context next to the plain message text.
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

### Dependencies / Assumptions

- Teams sends a `text/html` attachment on channel messages from desktop and web, including
  unformatted ones. Recorded in the channel-file spike and Microsoft's sample payload; editors use
  exactly these clients.
- The mention appears in the HTML as a `span` with
  `itemtype="http://schema.skype.com/Mention"` (Microsoft samples). Unconfirmed in our tenant.

### Outstanding Questions

**Deferred to Planning**

- Capture real activities from our tenant: a mention, a pasted title-converted link, a pasted Word
  draft, and a plain typed message. They settle the mention markup, how large a Word paste is, and
  whether Teams adds `style` to plain typed messages, which would make R1's formatting check fire
  on everything.
- Which tags and attributes count as formatting for R1.

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
