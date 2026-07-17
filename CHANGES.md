# Changes

Downstream-only changes on top of upstream cc-connect, grouped by the upstream base
they sit on (newest first). See `DOWNSTREAM.md` for the versioning scheme and how
this file evolves across re-bases. Inline `(upstream: …)` marks each item's
upstreaming status: `pending` (not submitted) · `submitted #N` · `merged`.
`(backport: upstream #N)` marks an upstream PR we carry early — not ours to
upstream; it drops when that PR lands upstream and we re-base.

## v1.5.0-beta.2

### Unreleased

#### Fix

- **teams**: reconstruct serviceURL for proactive sends (upstream: pending)
- **claudecode**: AskUserQuestion reaches its interactive prompt in `dontAsk` /
  `bypassPermissions` modes instead of being auto-decided (upstream: pending)
- **claudecode**: resume sessions against the agent's real working directory
  (upstream: pending)
- **core**: open the API control socket to `run_as_user` agents
  (upstream: pending; rel. chenhg5#1527)
- **core**: stop duplicating pre-boundary text on non-preview streaming cards
  (upstream: submitted #1528)
- **display**: quiet-mode streaming-card NO_REPLY handling
  (upstream: pending; rel. chenhg5#1302)
- **slack**: ignore edited @mentions instead of re-running the agent
  (upstream: pending)
- **slack**: working-indicator reactions target the summoning message, not the
  thread root (backport: upstream #1523)

#### Feature

- **teams**: Microsoft Teams connector — inbound/outbound messaging, streaming
  Adaptive-Card replies, and reading files attached to the bot in a channel via
  Microsoft Graph (upstream: submitted #1518)
