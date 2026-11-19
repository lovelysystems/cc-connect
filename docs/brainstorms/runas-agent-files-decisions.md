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

## Build

## Open questions
