# Unit brief: runas-agent-files

When a project sets `run_as_user`, cc-connect does its work on the agent's files as the agent's
user, never as root, so an agent cannot get a command or a write run as root by planting files in
its own folders.

Story: lovelysystems/ch#2022
Workpackage: ch#2022 WP2
Task: lovelysystems/ch#2321

Base branch: `downstream`. The pull request targets `downstream`, never `main`: `main` only
mirrors upstream (`DOWNSTREAM.md`, "Branches").

`docs/plans/` on this branch holds upstream's own plan files. None of them is this unit's plan;
planning for this unit has not run yet.

## Acceptance criteria this unit serves

ch#2022 and ch#2023 are planned as a pair. This unit serves:

- ch#2022: "The publication goes live only after the editor confirms the summary"
- ch#2023: "The limit also holds when the bot itself is prompt-injected"

How it serves them: the content bot runs its agent as the user `claude`, and cc-connect runs as
root in the same pod. A root-only program in that pod will hold the only key that can push the
live content, and only a button click in Teams makes it push. Any way for the agent to get root
reaches that key and publishes without the click and past the limit. This unit closes the three
known ways.

## This unit's slice

When a project sets `run_as_user`, cc-connect does each of these as that user:

- **Runs Claude settings hooks.** It reads the PermissionRequest hooks from the settings files and
  runs them with `sh -c` as root today (`agent/claudecode/cc_hooks.go:108-160`, `:255`). The agent's
  user writes those files: the session folder can hold its own `.claude/settings*.json`, and in
  the bot's pod one volume is mounted at both `/home/claude/.claude` and `/root/.claude`.
- **Runs the agent binary for the usage readout.** It starts `claude` in a pty as root, which reads
  the same settings (`agent/claudecode/claude_usage.go:61-90`).
- **Writes attachments.** It writes inbound images and files into
  `<workDir>/.cc-connect/attachments` as root (`agent/claudecode/session.go:968-981`;
  `core/message.go:157`, which every agent calls). The agent's user writes that folder, so a planted
  link redirects the write.

A project without `run_as_user` keeps today's behaviour for all three.

Cases, each with its own test:

- A settings hook command runs as the `run_as_user` user.
- A hook defined in the session folder's `.claude/settings.json` or `settings.local.json` runs as
  that user, never as root.
- The usage readout's `claude` process runs as that user.
- An inbound image is written as that user.
- An inbound file is written as that user.
- A symlink planted in the attachments folder, or as the folder itself, is never followed by a
  root write.
- Without `run_as_user`: hooks, usage readout, image and file writes behave as today.

## The decisions this brief prefers

- Projects that set `run_as_user` keep their hooks; the hooks run as the agent's user. Preferred
  over an option that turns settings hooks off, which fails open on a typo in the config and
  covers only one of the three actions.
- The change stays inside cc-connect. Preferred over moving the pod's volume mount, which needs a
  path setting for cc-connect's session check and covers only one of the three too.
- cc-connect already spawns the agent itself as the `run_as_user` user (`core/runas.go`,
  `agent/claudecode/session.go:327-400`), with a check before each spawn. That is how the repo
  already does this job.
- The change is a bug fix in our fork, offered upstream later as one: upstream calls `run_as_user`
  an isolation boundary (chenhg5/cc-connect#496). The fork carries it whether or not upstream takes
  it.

## How this repo wants changes made

- One pull request into `downstream`, squash-merged.
- Written PR-ready for upstream: upstream conventions, tests, no downstream-only glue
  (`DOWNSTREAM.md`, "Goal: dissolution").
- A `CHANGES.md` entry under `## v1.5.0` → `### Unreleased` → `#### Fix`, in the style
  `- **scope**: description (upstream: pending)`. Never edit `CHANGELOG.md`; upstream owns it.
- `docs/usage.md`, section "Running agents as a different Unix user (`run_as_user`)": update it
  where it describes what runs as which user.
- `go test ./...` passes before each commit (`CLAUDE.md`).

## Cross-cutting constraints

- `run_as_user` becomes a precondition another unit relies on. A later unit in this repo adds a
  card route, `POST /signoff/send`, which refuses to post for a project without `run_as_user`.
  That refusal only means something once this unit has made `run_as_user` hold for these three
  actions.
- Release order: this cc-connect change is released first, then the bot's base image raises its
  cc-connect pin, then the root-only program that pushes the live content can merge. An older
  cc-connect still does these actions as root, and the bot could then reach root.

## What to leave alone

- **The slash commands that touch the work dir as root:** `/memory show` and `/memory add`
  (`core/engine.go:13885-13930`), `/diff` (`core/engine.go:8172-8192`), `/show`, `/shell`. Every
  slash command is disabled in the content bots (`disabled_commands = ["*"]`), so a tricked agent
  cannot reach them there. They are named as remaining work when this change is offered upstream,
  not built here.
- **The card route `POST /signoff/send`, its click handling and its refusal for projects without
  `run_as_user`:** another unit in this repo builds them.
- **Other agents' sessions:** `run_as_user` exists for the Claude Code agent only.
  `core/message.go`'s shared writer may change, but the other agents keep today's behaviour.
- **The upstream pull request itself:** it is posted by hand after this merges.
- **`main`:** no commit lands there.

## Capture instruction

> Whenever you make a choice this brief and the unit plan do not dictate, and a reasonable
> implementer could have gone the other way, append one line to
> `docs/brainstorms/runas-agent-files-decisions.md`: what you chose, the alternative you rejected,
> one clause of why, and `file:line`. Write it as you go, never reconstructed at the end — a
> decision looks inevitable once the code around it exists, which is exactly how it escapes
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
> question under `## Open questions` in `docs/brainstorms/runas-agent-files-decisions.md`, commit
> and push, and end the run. Otherwise decide, log one line, and carry on.
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

- Anything cc-connect runs, reads or writes as root on a path the agent's user can write. (This
  unit closes the three actions in its slice; the slash commands are left alone, as stated above.)
- Anything that lets the agent's user read the live key, run the root-only publish command, or
  reach that program's own directories.
- The route `POST /signoff/send` and its fields: `session_key`, `request_id`, `nonce`, `title`,
  `body`, `confirm_label`, `cancel_label`; and the check that the caller belongs to `session_key`.
- The `body` format: plain-text lines with a style from a fixed set, nothing that clicks, nothing
  in any platform's card format. The new `core.Card` element they become.
- Teams implementing `CardSender`, or any other card drawn through the signoff interface.
- The config key `signoff_exec`.
- The button value `signoff:<request_id>:<nonce>`, and any path by which typed text or "Allow all"
  resolves it.
- Where a signoff click is handled: in the Teams handler before any message is built, only in the
  thread its card was posted in, kept in memory, never written to disk. The click never reaches
  the agent; only the outcome report does, after the gate has answered.
- The outcome report cc-connect hands the agent: fixed text, the outcome and the gate's `message`,
  never the nonce.
- An outcome turn dropped because the session is busy.
- What `signoff_exec` gets on stdin (`request_id`, `nonce`, `clicked_by`) and answers on stdout
  (`outcome`, `message`).
