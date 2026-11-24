# Unit brief: data-dir-hardening

When a project sets `run_as_user`, its agent's user can enter `data_dir` but cannot read anything
cc-connect keeps there: no other project's session history, jobs, bindings, logs or secrets.

Story: lovelysystems/ch#2023
Workpackage: ch#2022 WP11 (the "offer upstream" part)

Base branch: `taras/runas-commands-and-socket` (PR lovelysystems/cc-connect#10) while that PR is
open; `downstream` once it is merged. Never `main`: `main` only mirrors upstream (`DOWNSTREAM.md`,
"Branches").

No pull request is open for this branch yet. Open one when the work is done, stacked on #10 or on
`downstream`, whichever is current.

## Why this unit exists

PR #10 lets `run_as_user` agents use the API socket. To reach `<data_dir>/run/api.sock` and
`<data_dir>/agent-prompts/`, every agent user must traverse `data_dir`. cc-connect writes its stores
there `0644` in `0755` folders, so every agent user can read them by name, including every
project's session history (`sessions/<project>_<hash>.json`; the hash is 4 bytes of sha256 of the
work dir, so the name is guessable).

PR #10 ships that as a documented limitation: the content bot runs one project per cc-connect, so
everything its agent can read is its own project's data. Upstream is different:

- Upstream's `docs/usage.md:324-329` ("Security guarantee and non-guarantee") promises an agent
  cannot read "another project user's" files.
- Upstream's socket is `0600` (`origin/main:core/api.go:80`), so upstream agents cannot reach
  `data_dir` today.
- Upstream deployments are usually multi-project.

So #10 must not be offered upstream without this unit. PR #10's description says so.

## What is already on this branch

Commit `923a0922` (a cherry-pick of `3aeddd61`, which was built, reviewed and reverted on #10 to
keep #10 small). It does:

- **Write modes.** Every store writes its file `0600` and creates its folder `0700`: core
  (`session.go`, `cron.go`, `timer.go`, `relay.go`, `projectstate.go`, `dir_history.go`,
  `heartbeat.go`, `workspace_binding.go`, `engine.go` `restart_notify`), `platform/teams`
  (`session.go`, `convref.go`), `platform/weixin`, `platform/qqbot` (its cache now goes through
  `core.AtomicWriteFile`), `agent/opencode`.
- **A startup sweep.** `cmd/cc-connect/datadir_modes.go` `tightenDataDir`, called from `main.go`
  after `setupLogger`, in every deployment: walks `sessions`, `projects`, `crons`, `timers`, `teams`,
  `weixin` (folders `0700`, files `0600`), every `*.json` directly in `data_dir`, and
  `run/qqbot_message_cache.json`.
- **A config check.** `cmd/cc-connect/runas_startup.go` `checkConfigFile`: under `run_as_user` only,
  startup stops when the config file is inside `data_dir` and group or others can read it. The
  aggregate error now reads "found %d problem(s)".
- **Tests.** One or two per store, the sweep, the config check, and a `tests/runas` check that
  `agent_a` and `agent_b` get "Permission denied" reading `sessions/*.json` and `crons/jobs.json`.
- **Docs.** The `data_dir` caveat in `docs/usage.md` narrowed; one `CHANGES.md` line.

Rebase it onto the current base first; #10 may have moved.

## What this unit still has to do

### 1. Replace the startup sweep with per-store tightening on load

Upstream would push back on `tightenDataDir`:

- it names `teams`, a downstream-only platform (`platform/teams` is not on `origin/main`), so the
  upstream copy would need a hand edit;
- it is a central list in `cmd/` of other packages' file names, which every new store must
  remember to join;
- it chmods every `*.json` in `data_dir`, including files the user put there.

Instead: each store, when it loads, sets its own folder `0700` and its own file `0600`. Each store
already has a load path (`core/cron.go:134`, `core/session.go:790`, `core/timer.go:109`, and the
matching ones in relay, project state, folder history, heartbeat, workspace bindings, the Teams,
weixin and qqbot stores, opencode). A missing file is skipped; a chmod failure is logged with
`slog.Warn`, never fatal. One shared helper in `core/`, beside `AtomicWriteFile`, used by every
store. The Teams lines stay in `platform/teams`.

Then delete `cmd/cc-connect/datadir_modes.go`, its call in `main.go` and its test.

### 2. The log file

With the default `data_dir` (`~/.cc-connect`), the daemon log `logs/cc-connect.log` is written
`0644`. It can carry other projects' session keys and message details. Write it `0600` and its
folder `0700`, and tighten an existing one when it is opened.

### 3. The user's config folder

`<data_dir>/config/` holds files the user puts there, such as the MiniMax API key file
(`config.LoadMiniMaxLocalConfig`, `cmd/cc-connect/main.go:816`). Agent users can read them. Set the
folder `0700` when it is read, and say so in the docs.

### 4. Docs

- `docs/usage.md`, the `run_as_user` security section: stores are `0600`; what stays readable to
  every agent user is the per-spawn prompt files in `agent-prompts/` (system and platform prompts
  of every project); `cc-connect agent-sid` does not work for a `run_as_user` agent (it lists
  `<data_dir>/sessions/` directly, `cmd/cc-connect/session_id.go:79`) — as in upstream today, where
  such an agent cannot reach `data_dir` at all.
- `CHANGES.md`: keep the one short line; append after existing entries in the Unreleased section.
- PR #10's description: once this lands, its "Security surface" entry no longer holds; update it
  or say so in this unit's PR.

## The decisions this brief prefers

- `0600` files and `0700` folders in every deployment, with or without `run_as_user`. cc-connect is
  the only reader; two write paths per store would gain nothing.
- `<data_dir>/run/` and `<data_dir>/agent-prompts/` keep their modes: agent users must reach the
  socket and read the prompt files.
- The config check stays as built: `run_as_user` only.
- Downstream-only code stays in downstream-only files, so the upstream copy needs no edit
  (`DOWNSTREAM.md`).

## Left out, documented only

- The doctor audit folder (`~/.cc-connect/audits`).
- A weixin `state_dir` override that points inside `data_dir` under another folder name.
- The default config file cc-connect writes on first start (`0644`): the config check already
  refuses it under `run_as_user` when it sits inside `data_dir`.
- Moving `agent-sid` onto the socket.

## Open for the upstream offer

- The per-spawn prompt files in `agent-prompts/` stay readable to every agent user. Either state it
  in upstream's docs as the one remaining caveat, or write them into a folder per target user
  (roughly 30 lines in `agent/claudecode/session.go:93-130`). Decide before offering upstream.

## Done when

- A test per store: after load and after save, the file is `0600` and its folder `0700`,
  independent of the umask; a pre-existing `0644` file is `0600` after load.
- The log file and the config folder have the same tests.
- `checkConfigFile` tests as on the branch.
- `tests/runas` (build tag `runas`) passes in a `golang:1.25` Linux container, with its
  "Permission denied" checks. CI does not run it on this branch; run it by hand.
- `go test` with `-run` filters on the touched packages, and
  `golangci-lint run --new-from-rev origin/downstream` on them, are clean.

## Capture instruction

> Whenever you make a choice this brief and the unit plan do not dictate, and a reasonable
> implementer could have gone the other way, append one line to
> `docs/brainstorms/data-dir-hardening-decisions.md`: what you chose, the alternative you rejected,
> one clause of why, and `file:line`. Write it as you go.

### Tripwire list

- Anything cc-connect reads or writes as root on a path the agent's user can write.
- Any change to `<data_dir>/run/` or `<data_dir>/agent-prompts/` modes.
- Any change visible to a deployment without `run_as_user` beyond file and folder modes.
