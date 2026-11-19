# runas-agent-files decisions

## Plan

- Every action under `run_as_user` is a process spawned through `core.BuildSpawnCommand` after `core.VerifyRunAsUserCheap`, over root-side chown or symlink-safe opens; it works for a non-root supervisor and a link then reaches only what the user can reach. `core/runas.go:155`, `agent/claudecode/session.go:331-396`
- Hook commands, destination paths and probe flags cross sudo in preserved env vars read as `"${NAME}"` or inside the spawned script, data on stdin, over passing them as argv; `sudo -i` backslash-joins argv for the login shell, dropping empty args and newlines and expanding `$NAME`. `core/runas.go:173-177`, `core/runas_audit.go:197-201`
- The chdir wrapper changes to `"${CC_RUNAS_CHDIR}"`, over leaving `"$CC_RUNAS_CHDIR"`, which the login shell expands unquoted and splits on spaces; the new hook spawn and the agent spawn use it. `core/runas.go:174`
- Settings files that define hooks are read as the user, in one spawn per session with marker-framed output, over reading them as root; the tripwire list names reads on agent-writable paths and a non-root supervisor cannot read the target's home. `agent/claudecode/cc_hooks.go:110-160`, `core/runas_audit.go` `parseProbeOutput`
- Under `run_as_user`, hook settings come from the target user's config home, over the supervisor's `~/.claude`; Claude spawned as the target reads that one and `docs/usage.md` tells operators to put hooks there. `agent/claudecode/claudecode.go:1046`, `docs/usage.md:458-460`
- Hooks under `run_as_user` run with the session work dir as cwd, over `sudo -i`'s HOME; Claude Code runs hooks in the project dir. `core/runas.go:165-173`
- Hook spawns under `run_as_user` cancel with SIGTERM and set `WaitDelay`, over today's SIGKILL-only cancel; sudo cannot relay SIGKILL and a surviving child would block `Wait` past the 60s timeout. `agent/claudecode/cc_hooks.go:243-262`, `core/hooks.go:180`
- The usage probe runs in the target's `${HOME}/.cc-connect/usage-probe`, made by `mkdir -p` in the same spawn, over a temp dir made and removed by extra spawns (three spawns, leaks on kill, a trust entry per run). `agent/claudecode/claude_usage.go:65-77`
- New spawns leave `cmd.Dir` unset, over copying the agent spawn's `cmd.Dir = workDir`; `sudo -i` ignores it and a non-root supervisor may not be able to enter the work dir. `agent/claudecode/session.go:364`
- Attachments go through one write-as-user primitive in `core` and a `SaveFilesToDisk` variant taking `SpawnOptions`, over branching in every agent or changing `SaveFilesToDisk`'s signature; other agents keep their call unchanged. `core/message.go:157`
- The write primitive uses noclobber with a distinct exit code for an existing name and Go keeps choosing names, over the shell picking a free name and reporting it; the existing suffix loop stays. `core/message.go:220-249`
- Under `run_as_user`, both the scoped and the flat attachment layouts use the exclusive create, over porting the temp-file-plus-hard-link path; both refuse to overwrite. `core/message.go:199-210`
- A failed sudo or per-spawn check fails the action (hook falls through to the prompt, usage errors, attachment skipped), over falling back to the supervisor. `agent/claudecode/cc_hooks.go:94-99`
- The write script returns its "exists" code only after an explicit `-e`/`-L` test of the destination, every other failure maps to skip, over reading any failed noclobber create as "exists"; that would spin the suffix loop on an unwritable dir. `core/message.go:221-233`
- A root-owned `.cc-connect` left by an older version is handled by a docs `chown -hR` step (link-safe, since the target can replace the dir with a symlink) and the existing skip log, over a root chown at runtime, which would be a root action in an agent-writable parent. `agent/claudecode/session.go:968-971`
- Attachment write failures stay log-and-skip with no chat message or inline-image fallback, over new user-facing text that needs five translations. `agent/claudecode/session.go:979-982`
- Tests prove "as the user" with a fake `sudo` first on `PATH` that reproduces `sudo -i` argv joining, over a command-builder seam in production code; `BuildSpawnCommand` and `ExecSudoRunner` resolve `sudo` through `PATH`. `core/runas.go:178,213`
- Each test package carries its own copy of the fake `sudo` shim, over a shared exported test-helper package. `core/runas_test.go`, `agent/claudecode/cc_hooks_test.go`
- Session-file operations under the supervisor's `~/.claude/projects` (`ListSessions`, `GetSessionHistory`, `DeleteSession`, `ValidateSessionID`, `transcriptPath`) are left alone and named for the upstream PR with the slash commands, over widening this unit; they sit outside the brief's three actions. `agent/claudecode/claudecode.go:461,570,627,731`, `agent/claudecode/session.go:1139`

## Plan review

- Cut: hooks under `run_as_user` running with the session work dir as cwd (old KTD5 clause, U3 step 3's `WorkDir`, U3's cwd test); nothing in the story requires it now: today hooks run in cc-connect's own cwd with no `cmd.Dir` and get the work dir as `cwd` on stdin. `agent/claudecode/cc_hooks.go:255`
- Cut: the chdir wrapper fix `"${CC_RUNAS_CHDIR}"` (old KTD3, U1 step 1, U1's space test); nothing in the story requires it now: with the hook cwd cut no new spawn sets `WorkDir`, and the split is inherited from the base and fails closed (a probe of sudo's argv escaping shows dash as login shell refusing the agent spawn on a work dir with a space, bash and zsh not splitting); name it for the upstream PR with the other remaining work. `core/runas.go:174`
- Changed: KTD numbers after the cut shift down by one (old KTD4–KTD8 are now KTD3–KTD7).
- Changed: the hook `WaitDelay` is pinned to 2s, the value the repo's other shell hook uses, where the plan named no number. `core/hooks.go:180`
- Changed: U5 adds the `#### Fix` heading under `### Unreleased`, which does not exist yet. `CHANGES.md:18-20`
- Kept: payloads in preserved env vars and data on stdin, over argv; R1's unchanged hook text needs it, since sudo backslash-escapes every byte but alphanumerics, `_`, `-` and `$`, so a newline becomes a line continuation and `$x` expands in the login shell (reproduced in a probe). `core/runas.go:173-177`
- Kept: under `run_as_user`, hook settings read as the user from the target's config home, over the supervisor's; the tripwire list names root reads on agent-writable paths, the target generally cannot read the supervisor's home, and `docs/usage.md:458-460` already tells operators to put hooks there; a hook missing after the switch falls through to the platform prompt. `agent/claudecode/cc_hooks.go:131-144`

## Build

- Units built inline one after another, over parallel workers; the Claude Code units share `session.go` and one fake-`sudo` shim in the test package, and U2 needs U1. `agent/claudecode/session.go`
- `SaveFilesToDisk` keeps its doc and delegates to `SaveFilesToDiskAs` with empty options, over a second copy of the body; without `run_as_user` the same `os` calls run in the same order. `core/message.go:160`
- The run-as-user suffix loop is its own function, over parameterising `writeUniqueNoOverwrite` with a writer; the plain path's code stays as it was. `core/message.go:275`
- The write script leaves the target user's umask alone, over forcing `umask 022`; the files are for that user's own agent. `core/runas.go:199`
- The write script creates the file with `: >` under noclobber, then appends stdin and removes the file if the append fails, over one `cat >` that would leave a partial file; a failed create never reaches the `rm`, so a name another writer took is never removed. `core/runas.go:202`
- `WriteFileAsUser` makes the destination absolute against the supervisor's cwd before the spawn, over passing it as given; `sudo -i` runs the script in the target's HOME, where a relative path would land elsewhere. `core/runas.go:214`
- The core fake-`sudo` tests sit in `core/runas_test.go` (built on non-Windows only), over `core/message_test.go`, which also builds on Windows and cannot see the shim. `core/runas_test.go:346`
- Hooks under `run_as_user` run in their own `runHookCommandAsUser`, over adding options to `runHookCommand`; the plain path's code and its tests stay as they were. `agent/claudecode/cc_hooks.go:416`
- The hook's own stdout is framed by marker lines too, over parsing all of sudo's stdout; a line of login-profile output would otherwise turn every decision into a parse error. `agent/claudecode/cc_hooks.go:35`
- The wrapper runs as `/bin/sh` and the hook as `sh` from the target's PATH, over `/bin/sh` for both; the hook keeps the `sh` it gets today. `agent/claudecode/cc_hooks.go:35`
- A failed settings read as the user leaves the session without hooks (once per session, like a missing file), over retrying on the next permission request; every request then goes to the platform prompt. `agent/claudecode/cc_hooks.go:181`
- The settings read gets its own 30s timeout, over the session context alone; a hung sudo would otherwise block the first permission request. `agent/claudecode/cc_hooks.go:213`
- `readSettingsFile` keeps reading the file and hands the bytes to a new `parseSettings`, over a second copy of the JSONC parse for framed output. `agent/claudecode/cc_hooks.go:294`

## Open questions
