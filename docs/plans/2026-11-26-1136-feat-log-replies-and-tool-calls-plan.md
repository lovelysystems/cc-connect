---
title: Transcript Log for Replies and Tool Calls - Plan
type: feat
date: 2026-11-26
topic: log-replies-and-tool-calls
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-brainstorm
execution: code
---

# Transcript Log for Replies and Tool Calls - Plan

## Goal Capsule

- **Objective:** An operator can review one prod chat session of the RegioTV content-bot from
  Loki alone: every user message and reply in full, and every tool call with its input, outcome
  and duration.
- **Means:** an opt-in transcript log in cc-connect, off by default, built on `downstream`.
- **Product authority:** [lovelysystems/ch#2398](https://github.com/lovelysystems/ch/issues/2398).
  Needed for the RegioTV launch on 1 December 2026.
- **Open blockers:** none.

---

## Product Contract

### Summary

A new config switch turns on a transcript log. When it is on, cc-connect writes every user
message and reply in full, and every tool call, to the same output as its operational log.
Debug-level logging no longer carries chat text. The noisy per-event lines of the Claude Code
agent go away.

### Problem Frame

Today the bot's prod logs cut each reply at 500 characters and never name the tools it calls.
On 24 November, 17 of 27 replies were longer than that, so the end of each one was lost: the
bot's question, its explanation, its next step. Nobody can tell which command was denied in
don't-ask mode (7 times so far), which `content` subcommand failed, or why one title lookup
took 25 tool calls and 79 seconds.

At the same time, upstream (chenhg5/cc-connect) is removing content from logs, at Debug level
too. It merged about 15 redaction PRs on 24–25 November and added a CI gate (`cmd/logcheck`)
that rejects payload variables in `slog` calls. Open PR #2330 would strip every slog attribute
that is not on an allowlist. A change that widens content in the operational log conflicts
with that direction, and once #2330 lands upstream, our rebases would strip such content
without any error.

### Key Decisions

- **Build on `downstream` now; propose upstream later as an issue, not a PR.** Upstream's
  current rule forbids content in logs, and an upstream PR could not merge before the launch.
  (session-settled: user-approved — chosen over an upstream PR first: it argues against the
  maintainer's redaction sweep and misses 1 December)
- **A dedicated switch, not a log level.** A trace level would mix content with other trace
  output, and #2330's allowlist would strip it. A switch that names what it turns on is also
  the shape we would propose upstream. Governs R1, R2. (session-settled: user-approved —
  chosen over a new trace level: same cost, and survives upstream's allowlist)
- **One switch named `transcript`, covering messages and tool calls.** A session review needs
  both, and both carry the same kind of content. Governs R1. (session-settled: user-approved —
  chosen over separate switches for messages and tool calls, and over the name `conversation`:
  prod would always set both, and `conversation` reads as messages only)
- **Replace the 500-character debug lines for replies and user messages.** Keeping them would
  log chat text twice and leave it at debug. Governs R3, R4, R8. (session-settled:
  user-approved — chosen over leaving the lines alone to avoid rebase conflicts, and over
  keeping user messages at debug: the conflict is small, and a reviewed session needs both
  sides in full)
- **Agent-agnostic, in core.** Every agent already reports tool calls and results to the
  engine, so the transcript log works for all agents and stays proposable upstream. The agent
  supplies what only it knows, such as the call ID that pairs a call with its result.
  Governs R5.

### Requirements

**Switch and output**

- R1. `transcript = true` under `[log]` turns the transcript log on for user messages, replies
  and tool calls together. It is off by default, and existing configs behave exactly as before.
- R2. When on, transcript records go to the same output as the operational log, whatever the
  log level, and survive a slog attribute allowlist. Each record is filterable in LogQL as its
  own kind and carries the session ID.

**What is recorded**

- R3. Every inbound user message is recorded in full.
- R4. Every reply is recorded in full, once per turn.
- R5. Every tool call is recorded with: the tool name, its input summary capped at about 300
  characters, ok or error, the exit code when the agent reports a real one, and the duration
  from call to result. Durations stay correct when tools run in parallel.
- R6. An errored tool call also carries the start of its error text, capped like the input, so
  a permission denial can be told apart from a failed command.
- R7. Tool input passes through the existing secret redaction before it is recorded.

**Removed noise**

- R8. The debug lines `turn response` and `message content` are removed. With the switch off,
  no log line carries user or reply text.
- R9. The Claude Code agent no longer writes one `claudeSession: event type=…` debug line per
  stream event.

### Acceptance Examples

- AE1. Switch off. **Covers R1, R8.**
  - **Given** the switch is unset and `log.level = "debug"`,
  - **When** a user sends a message and the bot answers with a 2,000-character reply after
    three tool calls,
  - **Then** the log has no user text, no reply text and no transcript records.
- AE2. Switch on, parallel tools. **Covers R2, R5.**
  - **Given** the switch is on and `log.level = "info"`,
  - **When** the agent runs two Bash commands in parallel and the second one finishes first,
  - **Then** two tool-call records appear, each with its own command and its own duration.
- AE3. Denied command. **Covers R5, R6.**
  - **Given** the switch is on and the bot runs in don't-ask mode,
  - **When** the agent tries a command outside its allowlist,
  - **Then** the tool-call record shows the command, the outcome error, and the start of the
    denial message.

### Scope Boundaries

- Logging inbound attachments (type, name, size) and session restores is deferred. The issue
  marks both optional.
- The upstream issue proposing the transcript log is deferred until after launch, so it can
  cite prod examples.
- A per-project switch is deferred. One global switch covers the content-bot.
- Tool output beyond the error excerpt in R6 is not recorded.

### Dependencies / Assumptions

- Loki collects cc-connect's stdout on op-prod. The transcript log adds no shipping config.
- Claude Code's stream sends `id` on each tool call and `tool_use_id` on its result.
  `agent/claudecode/session.go` reads neither yet.
- Claude Code reports no real exit code. Its tool results carry only `is_error`, and the agent
  turns that into 0 or 1. R5's exit code is therefore absent for Claude Code unless planning
  finds it in the result text.

### Outstanding Questions

**Deferred to Planning**

- How records reach the output without going through the default slog handler (R2).
- How the call ID travels from the agent to core: a new field on `core.Event`, or something
  else. Upstream PR #2160 adds call IDs to the same events; align with its naming if cheap.

### Sources / Research

- Downstream code: `core/engine.go` (`turn response` and `message content` debug lines),
  `agent/claudecode/session.go` (`claudeSession: event` line, `tool_use` and `tool_result`
  handling), `agent/claudecode/claudecode.go` (`summarizeInput`), `core/message.go`
  (`core.Event`), `core/redact.go` (`RedactArgs`), `config/config.go` (`LogConfig`).
- Upstream redaction direction: chenhg5/cc-connect PRs #2410 (ACP tool input), #2411 (platform
  message content), #2412 (Claude Code tool results), #2420 (`cmd/logcheck` CI gate), open
  #2330 (slog attribute allowlist).
- Upstream call IDs: chenhg5/cc-connect open PR #2160.
