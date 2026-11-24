# Unit brief: runas-commands-and-socket

When a project sets `run_as_user`, its agent can use cc-connect's API socket for its own project
and for nothing else, and every command cc-connect runs for that project runs as the agent's user,
so the agent cannot reach root or another project through the socket.

Story: lovelysystems/ch#2023
Workpackage: ch#2022 WP11
Task: lovelysystems/ch#2375

Base branch: `downstream`. The pull request targets `downstream`, never `main`: `main` only
mirrors upstream (`DOWNSTREAM.md`, "Branches").

`docs/plans/` on this branch holds upstream's own plan files. None of them is this unit's plan;
planning for this unit has not run yet.

## Acceptance criteria this unit serves

ch#2022 and ch#2023 are planned as a pair. This unit serves:

- ch#2022: "The publication goes live only after the editor confirms the summary"

How it serves it: the content bot runs its agent as the user `claude`, and cc-connect runs as root
in the same pod. The bot's publish command, running as `claude`, posts a confirmation card through
`POST /signoff/send` on the API socket, and on Approve cc-connect runs the project's
`signoff_exec`, which pushes the change live. Today the socket is `0600` and root-owned, so
`claude` cannot reach it, and `signoff_exec` runs as root, which would run code the bot's user can
change. This unit makes the socket reachable by `claude` for its own project and runs
`signoff_exec` as `claude`.

Everything else in the slice serves no criterion. It is settled anyway, for one reason: once the
agent can use the socket, any command cc-connect runs for the project as root (cron `exec` added
through the socket, above all) hands the agent root, and any route that trusts the project named in
the request hands it other projects.

## This unit's slice

### Commands run for a project

When a project sets `run_as_user`, every command cc-connect runs for that project runs as that
user. Today each runs as cc-connect's own user with its full environment:

- `signoff_exec` (`core/signoff_exec.go:41`; its config comment says "run as cc-connect's own
  user", `config/config.go:567-569`)
- cron `exec` (`core/engine.go:2043`) and timer `exec` (`core/engine.go:1865`)
- custom-command `exec` (`core/engine.go:14649`), `/shell` and `!` (`core/engine.go:8111`), all
  through `runShellWithProgress` (`core/engine.go:7887`)
- webhook `exec` (`core/webhook.go:259`)
- `[[hooks]]` commands (`core/hooks.go:174`; the manager is built per project at
  `cmd/cc-connect/main.go:587`)
- `/diff` (`core/engine.go:8154`) and the workspace clone (`core/engine.go:16909`), which run in the
  project's workspace

A project without `run_as_user` keeps today's behaviour for every one of them, byte for byte.

### Who may use the socket

On every request, cc-connect reads the caller's user id from the socket's peer credentials. A
caller may act only for projects whose agent runs as that user. cc-connect's own user and root may
act for every project, as today. Two projects that share a `run_as_user` share one scope.

Per route, for a caller other than cc-connect's own user and root:

- `/send`, `/cron/*`, `/timer/*`: only a project of the caller's. A job of another project answers
  as not found, with the same text as a missing job. A cron edit may only name a project of the
  caller's (`core/cron.go:308` lets an edit move a job today).
- `/relay/send`: only `from` a project of the caller's.
- `/signoff/send`: a project of the caller's, then the existing check that the caller runs under
  that session's agent.
- `/sessions`, `/relay/bind`, `/relay/binding`, and every route added later: nothing.

### The socket file and startup

- The socket stays `0600` while no project has `run_as_user`. With one, it becomes `0666`, after
  cc-connect has checked that no other user can write its folder or any folder above it. No
  `chown` anywhere.
- On a platform without peer credentials, the socket stays `0600` and every caller counts as
  cc-connect's own user; a project with `run_as_user` there stops cc-connect at startup.
- When a project has `run_as_user`, a socket that cannot be opened or made safe stops cc-connect at
  startup, with a message naming the fix. Today a failure only warns
  (`cmd/cc-connect/main.go:1261`). cc-connect also checks at startup that each project's agent user
  can reach the socket.
- The agent receives `CC_DATA_DIR`, `CC_PROJECT` and `CC_SESSION_KEY` under `run_as_user`. Today
  they are stripped (`core/runas.go:107-113`, `:255`), so the CLI looks for the socket in the wrong
  place (`cmd/cc-connect/send.go:350`).

### Cases, each with its own test

Commands:

- Under `run_as_user`, each command above runs as the target user: `signoff_exec`, cron `exec`,
  timer `exec`, custom `exec`, `/shell` and `!`, webhook `exec`, `[[hooks]]`, `/diff`, workspace
  clone.
- Without `run_as_user`, each runs as cc-connect's own user, as today.
- A command run as the target gets only what `run_as_env` passes, plus the `CC_*` variables a job
  needs to call the CLI back; never cc-connect's full environment.
- What the target's login shell prints before the command starts never reaches the chat or the
  command's captured output.
- A command that times out or is cancelled still stops.
- `signoff_exec` still reads the click on stdin and answers on stdout, unchanged.

Socket:

- cc-connect's own user and root: every route, every project, as today.
- A project's agent user: `/send` for its project succeeds; for another project it is refused; with
  no project named it defaults to its one project.
- A project's agent user: `cron add` and `timer add` for its project succeed, for another project
  are refused; `cron list` and `timer list` show only its jobs.
- A project's agent user on another project's job: `info`, `del`, `exec`, `run` answer as not
  found, the same as a missing job.
- A project's agent user: `cron edit <own job> project <other>` is refused.
- A project's agent user: `/relay/send` from another project is refused.
- A project's agent user: `/sessions`, `/relay/bind`, `/relay/binding` are refused; so is a route
  registered without a stated rule.
- A project's agent user: `/signoff/send` for its own project and session succeeds; for another
  project it is refused.
- A user that is no project's agent: refused on every route.
- Two projects with the same `run_as_user`: each caller acts for both.
- Socket mode: `0600` with no `run_as_user` project; `0666` with one.
- The socket folder is resolved through symlinks before it is checked; a folder above the socket
  that another user can write stops startup and names that folder; the socket file is checked
  again after its mode is set.
- A `data_dir` the agent's user cannot traverse stops startup with a message that names the fix.
- After a config reload, every caller's scope is unchanged.
- Under `run_as_user`, the agent sees `CC_DATA_DIR`, `CC_PROJECT` and `CC_SESSION_KEY`, and
  `cc-connect send` from the agent reaches the socket.

End to end:

- A real Linux test with several users proves each user controls only its own project: one user per
  project, one user that is no project's agent, and the matrix above run through the real CLI as
  each user. It runs twice, once with cc-connect as root and once with cc-connect as a non-root
  user given a scoped sudo rule. A cron `exec` job of a `run_as_user` project records that it ran
  as that user; one of a project without `run_as_user` records cc-connect's own user. Upstream's
  reviewer asked for exactly this before accepting any fix (see "Cross-cutting constraints").

## The decisions this brief prefers

- One place builds every command run for a project, with sudo, the way the agent itself is started
  (`core/runas.go:160`, `BuildSpawnCommand`, behind the cached check at `:305`). Preferred over a
  rule for `signoff_exec` alone, which the other `exec` keys would not follow.
- No setting for which user a command runs as. Nothing would set another value.
- Matching by user id alone. Upstream's docs draw the isolation line per user: projects sharing a
  `run_as_user` are not isolated from each other (`docs/usage.md:331`).
- Peer credentials over a token or a socket per project: the kernel says who called, nothing is
  handed to the agent, nothing changes on reload. `run_as_user` is set once at startup. The fork
  already reads peer credentials on Linux (`core/peercred_linux.go:27`) and stores the connection in
  the request context (`core/api.go:185`).
- Each request reads the project's user at that moment, so there is no permission state to update
  on reload.
- A route states its rule when it is registered; a route without one is for cc-connect's own user
  and root only.
- The non-root supervisor upstream documents (`docs/usage.md:402-408`) works with no new sudoers
  line: its documented scoped rule already lets it run any command as the target.
- `docs/usage.md`, "Security guarantee and non-guarantee" (`:324`): add that commands cc-connect
  runs for the project also run as the target user, and a part on who may use the socket, its mode
  and the startup checks.

## How this repo wants changes made

- One pull request into `downstream`, squash-merged.
- Written PR-ready for upstream: upstream conventions, tests, no downstream-only glue
  (`DOWNSTREAM.md`, "Goal: dissolution"). The change is offered upstream by hand after it merges,
  as the fix for chenhg5/cc-connect#1527.
- The signoff parts (`/signoff/send`'s scope check, `signoff_exec` as the target) touch fork-only
  code. Keep them separable from the rest, so the upstream offer can leave them out.
- `CHANGES.md` entries under `## v1.5.0` → `### Unreleased`, in the style
  `- **scope**: description (upstream: pending)`. The ls.4 entry for signoff cards stays as written;
  the new entry says `signoff_exec` now runs as the project's `run_as_user`. Never edit
  `CHANGELOG.md`; upstream owns it.
- `go test ./...` passes before each commit (`CLAUDE.md`).

## Cross-cutting constraints

- **Upstream already rejected a simpler fix.** chenhg5/cc-connect#1580 opened the socket to the
  `run_as_user` target by `chown` (one target) or a shared group with `0660`. The owner closed it
  over four blockers: `chown` fails with `EPERM` under the documented non-root supervisor and only
  warned, so the bug stayed; the socket has no caller check, so a sandboxed user controls every
  project; reload did not update access; the path change had no protection against a swapped
  directory or symlink. They asked for peer credentials checked against the project (or a socket
  or token per project), support for the non-root supervisor, an actionable startup error when
  `run_as_user` depends on the socket, and a real Linux multi-user test proving each user controls
  only its own project. Do not bring back `chown`, a shared group, or a failure that only warns.
- **chenhg5/cc-connect#1527 is closed** (stale bot). The upstream offer references it as closed or
  asks for it to be reopened; that is a manual step after the merge.
- **Release order.** Commands running as the project's user must land before or together with the
  socket rule: the socket rule alone lets the agent add a cron `exec` job that runs as root. Both
  are in this unit. Then cc-connect is released, lovely-docker-agent raises its cc-connect pin, and
  only then can ch-content-bot merge the publish command that posts the card.
- **The consumer.** ch-content-bot sets `run_as_user = "claude"` and
  `data_dir = "/var/lib/cc-connect"` (`ch-content-bot:master:docker/config.default.toml:15,48`);
  the base image keeps that folder root-owned and world-traversable
  (`lovely-docker-agent:master:docker/entrypoint.sh:45-49`). That setup must pass the folder check
  and the reachability check. Its publish command calls `POST /signoff/send` as `claude` and finds
  the socket and its session through `CC_DATA_DIR` and `CC_SESSION_KEY`; `signoff_exec`, set in its
  bridge config, runs as `claude` in the bot's copy of the content repository.
- **Accepted reach.** Once this lands, each bot can use `send`, cron and timer for the one project
  its cc-connect serves: post into any thread of that project and schedule its own turns. Its
  allow-list has no `cc-connect` grant, but other allowed commands can reach the socket. This is
  accepted; do not narrow it further in this unit.

## What to leave alone

- **The signoff route's refusal for a project without `run_as_user`** (`core/api.go:831`). Not part
  of this workpackage; it stays as is.
- **The rest of the signoff route and card**: its fields, the card, the click, the button values,
  the outcome report, the session hold. Only the scope check and the user `signoff_exec` runs as
  change.
- **Claude settings hooks, the usage readout and attachment writes**: already done as the agent's
  user by an earlier unit (`CHANGES.md`, ls.4 Fix entry).
- **ffmpeg, TTS, `doctor`, daemon management, self-restart**: they work on cc-connect's own files,
  not in a project's space, and stay as cc-connect's own user.
- **`disabled_commands` for socket callers**: not applied to the socket today; out of scope.
- **lovely-docker-agent's README**, which says agent-side `send`, `cron` and `timer` are
  unsupported: another repo.
- **ch-content-bot**: its publish command and `signoff_exec` setting are another workpackage.
- **The upstream pull request itself**: posted by hand after this merges.
- **`main`**: no commit lands there.

## Capture instruction

> Whenever you make a choice this brief and the unit plan do not dictate, and a reasonable
> implementer could have gone the other way, append one line to
> `docs/brainstorms/runas-commands-and-socket-decisions.md`: what you chose, the alternative you
> rejected, one clause of why, and `file:line`. Write it as you go, never reconstructed at the end
> — a decision looks inevitable once the code around it exists, which is exactly how it escapes
> notice.

This binds both planning and implementation. Planning writes what it pinned that this brief did
not under `## Plan`; implementation writes what it decided while building under `## Build`. One
file, one list.

## When to ask

> Ask when being wrong is expensive beyond this unit; log and continue when it is contained within
> it. Concretely, at the moment a choice appears: if what binds resolves it, it is not a decision —
> do it. If either option would contradict what binds, stop: that is a requirements defect, not a
> choice. If choosing wrong would cost work outside this unit — it crosses a unit boundary, touches
> anything on the tripwire list, or becomes visible outside the system at release — stop: write the
> question under `## Open questions` in `docs/brainstorms/runas-commands-and-socket-decisions.md`,
> commit and push, and end the run. Otherwise decide, log one line, and carry on.
>
> What binds is the acceptance criteria, the outcomes the requirements state, the contracts this
> brief names, and the tripwire list. Where this brief or the requirements name a mechanism —
> which component does the work, which fields, how many, validated how — that is a preference, not
> a settled decision. When the code supports a better shape for the same outcome — the way the repo
> already does the job, or a simpler one — build that, and log it in the decisions file with the
> mechanism it replaced and the `file:line` that supports it. Dislike of a preference is not
> evidence; the code is.

"The requirements" here means the acceptance criterion above, the slice and the constraints this
brief carries, and the story's PO answers.

### Tripwire list

The entries that concern cc-connect:

- Anything cc-connect runs for a project under `run_as_user` as any user other than that project's
  `run_as_user`.
- Anything cc-connect runs, reads or writes as root on a path the agent's user can write.
- Any socket route that acts for a project other than the caller's, for a caller other than
  cc-connect's own user and root; any route a caller other than those reaches by default.
- The socket's mode and the folder check before it opens; any `chown` of the socket.
- The variables the agent receives under `run_as_user`: `CC_DATA_DIR`, `CC_PROJECT`,
  `CC_SESSION_KEY`.
- The config key `signoff_exec`, and any setting for which user it runs as.
- What `signoff_exec` gets on stdin (`request_id`, `nonce`, `clicked_by`) and answers on stdout
  (`outcome`, `message`).
- The route `POST /signoff/send`, its CLI command `cc-connect signoff send`, and its fields:
  `session_key`, `request_id`, `nonce`, `title`, `body`, `confirm_label`, `cancel_label`; and the
  check that the caller belongs to `session_key`.
- The Approve button value `signoff:<request_id>:<nonce>`, the Cancel button's value bound to the
  same request and nonce, and any path by which typed text or "Allow all" resolves either.
- The outcome report cc-connect hands the agent: fixed text, the outcome and the command's
  `message`, never the nonce.
- An outcome turn dropped because the session is busy. The click holds the session until the
  outcome turn ends.
