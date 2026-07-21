# Changes

Downstream-only changes on top of upstream cc-connect, grouped by the upstream base
they sit on (newest first). See `DOWNSTREAM.md` for the versioning scheme and how
this file evolves across re-bases. Inline `(upstream: …)` marks each item's
upstreaming status: `pending` (not submitted) · `submitted chenhg5/cc-connect#N` ·
`merged`. `(backport: chenhg5/cc-connect#N)` marks an upstream PR we carry early — not ours to
upstream; it drops when that PR lands upstream and we re-base.

## v1.5.0-beta.2

### Unreleased

### 2026-07-17 / v1.5.0-beta.2-ls.1

#### Fix

- **claudecode**: AskUserQuestion reaches its interactive prompt in `dontAsk` /
  `bypassPermissions` modes instead of being auto-decided
  (upstream: pending; `bypassPermissions` half tracked by chenhg5/cc-connect#1533,
  `dontAsk` half not submitted)
- **claudecode**: resume sessions against the agent's real working directory
  (upstream: submitted chenhg5/cc-connect#1581)
- **core**: open the API control socket to `run_as_user` agents
  (upstream: submitted chenhg5/cc-connect#1580; closes chenhg5/cc-connect#1527)
- **core**: stop duplicating pre-boundary text on non-preview streaming cards
  (upstream: submitted chenhg5/cc-connect#1528)
- **display**: quiet-mode streaming-card NO_REPLY handling
  (upstream: pending; rel. chenhg5/cc-connect#1302)
- **slack**: ignore edited @mentions instead of re-running the agent
  (upstream: submitted chenhg5/cc-connect#1579)
- **slack**: working-indicator reactions target the summoning message, not the
  thread root (backport: chenhg5/cc-connect#1523)

#### Feature

- **teams**: Microsoft Teams connector — inbound/outbound messaging, streaming
  Adaptive-Card replies, interactive permission/question card buttons, inbound
  1:1 files/images, outbound inline images, and proactive sends via serviceURL
  reconstruction (upstream: submitted chenhg5/cc-connect#1518)
- **teams**: read files attached to the bot in a channel via Microsoft Graph
  (upstream: pending; follow-up to chenhg5/cc-connect#1518)
