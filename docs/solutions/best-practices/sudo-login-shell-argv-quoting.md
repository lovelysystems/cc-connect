---
title: Passing values through sudo -i to a run_as_user spawn
date: 2026-11-19
category: best-practices
module: core/runas
problem_type: best_practice
component: infrastructure
severity: medium
applies_when:
  - "Adding a spawn that goes through core.BuildSpawnCommand with run_as_user set"
  - "Writing a /bin/sh -c script that runs as the target user"
  - "A run_as_user test passes on macOS and fails in CI"
tags: [run-as-user, sudo, shell-quoting, login-shell, dash, bash-3-2]
---

# Passing values through sudo -i to a run_as_user spawn

## Context

With `run_as_user`, cc-connect starts every process as
`sudo -n -iu <user> --preserve-env=... -- <cmd> <args...>` (`core/runas.go:155`). With `-i`, sudo
does not exec the command directly. It backslash-escapes every byte of every argument except
alphanumerics, `_`, `-` and `$`, joins the arguments with spaces and hands that one string to the
target's login shell as `-c`. The fake `sudo` in the tests reproduces this
(`core/runas_test.go:285-293`).

What this does to an argument:

- An empty argument disappears.
- A newline becomes a backslash-newline, a line continuation, so multi-line text loses its line
  breaks.
- `$` is not escaped, so `$NAME` (a name made of letters, digits and `_`) is expanded by the login
  shell, outside quotes, because the quotes around it were escaped into literal characters. The
  value is then split on whitespace by a shell that field-splits.
- `${NAME}` and `"$@"` survive: the `{` or `@` after the `$` is escaped, so the login shell leaves
  the `$` alone and the inner `sh -c` script sees the text as written.

## Guidance

- Pass hook text, paths and other data in environment variables named in `--preserve-env`, or on
  stdin. Never in argv.
- Keep the spawned script on one line and read every variable as `"${NAME}"`, never `"$NAME"`.
  `writeAsUserScript` (`core/runas.go:197`), the hook and settings scripts
  (`agent/claudecode/cc_hooks.go:20`) and `usageProbeAsUserScript`
  (`agent/claudecode/claude_usage.go:51`) follow this.
- Frame anything the script prints between marker lines: the login profile can print too.

`TestWriteFileAsUser_PathWithSpaces` (`core/runas_test.go:392`) writes to a path with spaces.
Changing any `"${CC_RUNAS_WRITE_...}"` in `writeAsUserScript` to `"$CC_RUNAS_WRITE_..."` fails it
when the fake's `/bin/sh` is dash.

## Why This Matters

The login shell decides whether a mistake shows:

- dash (`/bin/sh` on Debian and Ubuntu, including CI's `ubuntu-latest`) field-splits the expanded
  `$NAME`, and the inner script fails with an unterminated quote.
- bash 3.2 (`/bin/sh` on macOS) did not split a word that also held an escaped `${...}` or `"$@"`
  in this session's probes, and zsh does not split unquoted expansions by default.

So a run_as_user test can pass locally on macOS and fail in CI. Only one of the four broken
variants of `writeAsUserScript` failed under macOS `/bin/sh`; all four failed under dash. Run the
fake-sudo tests on Linux, or with dash as the fake's shell, before trusting a quoting change.

The target user's login shell in a deployment decides the same way, so a mistake that a bash or
zsh login shell hides breaks a deployment whose target logs in with dash.

## When to Apply

- Any new `BuildSpawnCommand` call with `RunAsUser` set that carries data the caller chose.
- Any edit to the scripts named above.

## Examples

The chdir wrapper in `BuildSpawnCommand` (`core/runas.go:178`) still reads `"$CC_RUNAS_CHDIR"`, and
its comment (`core/runas.go:171-176`) says spaces survive sudo's re-quoting. They do not under a
dash login shell: with `CC_RUNAS_CHDIR=/tmp/a b`, dash hands the inner `sh -c` the script
`cd "/tmp/a`, which fails, so the agent spawn is refused. bash 3.2 and zsh started it in the right
directory. The fix is `"${CC_RUNAS_CHDIR}"`; it is still open. A test for it would call
`BuildSpawnCommand` with a `WorkDir` containing a space through the fake `sudo` and expect the
command to run in that directory.

```sh
# before: the login shell expands the value outside quotes
cd "$CC_RUNAS_CHDIR" || exit 1; exec "$@"
# after: the inner sh -c script expands it inside quotes
cd "${CC_RUNAS_CHDIR}" || exit 1; exec "$@"
```
