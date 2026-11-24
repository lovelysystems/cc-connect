---
title: Running a project command as run_as_user through a sudo login shell
date: 2026-11-24
category: design-patterns
module: core/runas
problem_type: design_pattern
component: service_layer
severity: high
applies_when:
  - "Adding a new place that runs a shell command for a project with run_as_user set"
  - "Changing projectCommandScript, buildProjectCommand or the framed output readers"
  - "Probing or trusting anything about the target user from cc-connect"
tags: [run-as-user, sudo, login-shell, process-group, sigterm, output-framing, peer-credentials]
---

# Running a project command as run_as_user through a sudo login shell

## Context

Under `run_as_user`, cc-connect runs project commands (`/shell`, custom exec, cron and timer
`exec`, webhook `exec`, `[[hooks]]`, `signoff_exec`, `/diff`, the workspace clone, the socket
probe) as the agent's user through `sudo -n -iu <user>`. Each naive way of doing that broke
while this was built, because `sudo -i` puts the target's login shell between cc-connect and the
command. The fixes live in one builder, `buildProjectCommand` in `core/runas.go`, and the
script it runs, `projectCommandScript`. This doc keeps why each part of that script is there,
so a later "simplification" does not reopen one of them.

## Guidance

Run every project command through `buildProjectCommand`, and read its output with
`commandLineWriter` (streaming) or `projectCommandOutput` (`core/project_command.go`). Do not
build a second `sudo` argv at a new site.

What the builder and script do, and the failure each part prevents:

- **Command text travels in env vars, not argv.** `sudo -i` joins argv into one line for the
  login shell and leaves `$` unescaped, so a `$X` in the command would expand in the target's
  login shell. The command, shell, flag and folder go in `CC_RUNAS_CMD`, `CC_RUNAS_CMD_SHELL`,
  `CC_RUNAS_CMD_FLAG` and `CC_RUNAS_CMD_DIR`, are added to the preserved allowlist, and the
  one-line script reads them only as `"${NAME}"` (comment on `projectCommandScript`). A dash login
  shell splits an unbraced `"$VAR"` with spaces (comment in `BuildSpawnCommand`).
- **The spawn argv starts with `exec`.** `buildProjectCommand` calls
  `BuildSpawnCommand(ctx, opts, "exec", "/bin/sh", "-c", projectCommandScript(pwsh))`. Per this
  build, dash (the `useradd` default login shell) otherwise keeps the script as a child, and the
  SIGTERM sudo relays reaches the login shell, never the script.
- **Cancel sends SIGTERM to sudo; the script kills the group.** sudo relays SIGTERM but not
  SIGKILL, and a non-root supervisor cannot signal the target's processes, so `cmd.Cancel`
  sends SIGTERM to sudo and `cmd.WaitDelay` is 2s. The script starts the command in its own
  process group (`setsid`, else `set -m`) and its TERM trap sends TERM, then KILL a second
  later, to that group.
- **Output is framed by marker lines.** The login shell may print before and after the command
  (profile, motd, logout). The script prints `@@cc-connect-cmd-begin` on stdout and stderr before
  the command and `@@cc-connect-cmd-end` on both after it; readers keep only what lies between.
  The begin markers print before the `cd`, so a folder the target cannot enter shows its `cd`
  error. The TERM trap prints the end markers before it exits: per this build, without that the
  login shell's logout output reached the timeout message.
- **stdin goes through fd 3.** A background command reads `/dev/null`, so the script copies
  stdin to fd 3 first (`exec 3<&0`) and starts the command with `<&3 3<&-`; `signoff_exec`
  writes its JSON on stdin.
- **`set +m` right after the start, `wait` to `/dev/null`.** Where `setsid` is missing (macOS),
  per this build bash prints a "Done" job line inside the stderr frame unless job control is
  turned off again.
- **stdout and stderr stay apart.** `2>&1` inside the frame was rejected: `signoff_exec` and
  `/diff` read stderr apart from stdout. Webhook `exec` and `[[hooks]]` keep `CombinedOutput`, and
  `projectCommandOutput` skips the second begin marker that one shared pipe carries.

Never trust what a command run as the target says about the target. The target's login profile
runs before the command and can shadow any tool on `PATH`. `ProbeSocketAccess`
(`core/runas_check.go`) reads the uid with `id -u -- <user>` run as cc-connect, and runs only
the folder and socket checks as the target. The first version ran `id -u` inside the target's
login shell, where a profile defining `id` could claim another uid's socket scope.

## Why This Matters

Each item above is a real failure seen in this build: a timed-out command left running, a
login banner in a chat reply, a `signoff_exec` that lost its stdin, a scoped caller claiming
another project's uid. Several only show with a real dash login shell or real sudo, which the
unit tests stand in for with a fake sudo.

## When to Apply

- Before editing `projectCommandScript`, `buildProjectCommand`, `commandLineWriter` or
  `projectCommandOutput`.
- When adding a site that runs anything for a `run_as_user` project.
- When cc-connect needs a fact about the target user (uid, groups, access): get it as cc-connect,
  or treat what the target reports as untrusted.

## Examples

Tests that hold these properties, all in `core/`:

- `fakeSudoScript`'s `noisy` mode in `core/runas_test.go` stays the parent like real sudo,
  relays TERM and prints before and after the command on both streams.
- `TestCmdShell_RunAsRepliesWithoutNoise`, `TestCmdDiff_RunAsBranchNameHasNoNoise` and
  `TestSignoffExecRunAsRunsAsProjectUserDespiteLoginNoise` check the framing.
- `TestRunShellWithProgress_RunAsTimeoutStops` checks that a timed-out command stops.
- `TestProbeSocketAccess_UIDIgnoresTheUsersProfile` in `core/api_socket_test.go` checks that a
  profile cannot change the uid.

The real multi-user test in `tests/runas` (build tag `runas`, Linux, as root; `make test-runas`)
runs the same paths with real users, real sudo and dash.

## Related

- `docs/usage.md`, the `run_as_user` section, for what users configure.
