# Decisions: runas-commands-and-socket

Choices the unit brief and the unit plan do not dictate, where a reasonable implementer could have
gone the other way. One line each: what was chosen, what was rejected, why, and `file:line`.

## Plan

- The project's user for commands and for the socket scope is the config's `run_as_user`, set on the engine at startup, not `engine.runAsUser()`; only claudecode reports the latter, so a codex project with `run_as_user` would run its cron `exec` as root (`core/engine.go:16630`, `cmd/cc-connect/main.go:365-372`).
- macOS reads peer credentials through `LOCAL_PEERCRED` (`golang.org/x/sys/unix.GetsockoptXucred`), not treated as a platform without them; making `run_as_user` fatal there would break a platform the docs support (`docs/usage.md:301`, `core/peercred_other.go:1`, `go.mod:98`).
- A username becomes a uid once at startup, by running `id -u` as that user through sudo, and is cached by name; `os/user.Lookup` per request was rejected because release builds use `CGO_ENABLED=0` and the pure-Go reader misses NSS/LDAP users (`Makefile:157`).
- A peer-credential read error on a platform that has them refuses the request; treating the caller as cc-connect's own user would fail open (`core/peercred_linux.go:27`).
- For a scoped caller, an unknown project and another project's name get the same 403 text, so the answer does not tell which names exist; 404 for unknown names was rejected (`core/api.go:255-261`). A job of another project gets exactly the answer that route gives for a missing id, status included (`core/api.go:474`, `:500-506`, `:572-574`, `:734`).
- `cron list` and `timer list` with no project return only the caller's jobs; with another project named they answer 403, the same as `cron add` (`core/api.go:438-444`, `:697-703`).
- A scoped caller's `/relay/send` with an empty `from` is refused; defaulting `from` to its one project was rejected because the CLI always sets it from `CC_PROJECT` (`cmd/cc-connect/relay.go:68-73`).
- `CC_PROJECT`, `CC_SESSION_KEY` and `CC_DATA_DIR` join `DefaultEnvAllowlist`; a per-agent allowlist in claudecode was rejected so every agent keeps them (`core/runas.go:107-113`).
- Relay-created sessions also get `CC_DATA_DIR`; without it the CLI looks for the socket in the target's home (`core/engine.go:15834-15845`, `cmd/cc-connect/send.go:346-358`).
- Every command run as the target gets `CC_PROJECT`, `CC_DATA_DIR` and, when one exists, `CC_SESSION_KEY`, plus the site's own variables (`CC_HOOK_*` for hooks); per-site subsets were rejected as rules nobody would remember (`core/hooks.go:243-268`).
- A command run as the target keeps each site's shell, flag, `shell_profile` and folder choices (timer keeps `sh`; `signoff_exec` keeps no profile and `/`), so the command string means what it meant before; dropping `shell_profile` under `sudo -i` was rejected (`core/engine.go:7858-7867`, `:1888`, `core/signoff_exec.go:60-61`).
- The command text and its work dir travel in preserved env vars read by a one-line `/bin/sh -c` script, not in argv or `SpawnOptions.WorkDir`; `sudo -i` leaves `$` unescaped and dash splits `CC_RUNAS_CHDIR` (`core/runas.go:176-179`, `agent/claudecode/cc_hooks.go:33-43`).
- The script prints a begin marker on stdout and on stderr before the command and an end marker on stdout after it; each stream keeps only what lies inside its frame. `2>&1` inside the frame was rejected because `signoff_exec` and `/diff` keep stderr apart from stdout, and a begin marker alone leaves trailing logout output in `signoff_exec`'s JSON (`core/signoff_exec.go:70-80`, `:111-113`, `core/engine.go:8188`).
- A command run as the target stops on timeout or cancel by SIGTERM to sudo, which relays it to the script; the script, running as the target, sends TERM and after 1s KILL to the command's own process group, and `WaitDelay` 2s bounds the rest. A process-group SIGKILL from cc-connect was rejected because sudo does not relay SIGKILL and a non-root supervisor cannot signal the target's processes (`core/runas.go:234-237`, `core/signoff_exec_unix.go:14-29`, `core/engine.go:8100`).
- The streaming sites (`/shell`, custom exec, cron, timer) read output through writers instead of pipes they drain to EOF, so `WaitDelay` bounds the wait; a project without `run_as_user` keeps SIGKILL on timeout (`core/engine.go:7911-7939`, `:2067-2110`, `:1894-1933`).
- `[[hooks]]` take the engine's run-as options when `Engine.SetHooks` attaches the manager; new `NewHookManager` arguments in `main.go` were rejected (`core/engine.go:874`, `cmd/cc-connect/main.go:587`).
- Under `run_as_user`, the workspace clone's existence checks and the skip-git `mkdir` run as the target with the clone; the brief names only the clone, but root stat and mkdir on a target-writable path trip the tripwire list (`core/engine.go:6796`, `:6818`, `:16796`).
- `diff2html` runs as the target with `/diff`'s git commands, and a target without it gets the text diff; keeping it as cc-connect's own tool was rejected because the tripwire list forbids running anything for a `run_as_user` project as another user (`core/engine.go:8259-8267`).
- The real multi-user test is a new `tests/runas` package behind a `runas` build tag plus a job in `.github/workflows/ci.yml`; widening CI's trigger to `downstream` was rejected as downstream-only workflow glue (`.github/workflows/ci.yml:3-7`, `DOWNSTREAM.md:36`).
- The end-to-end matrix covers `/signoff/send` only through its refusals; its success path needs a card platform and is covered by the unit tests (`core/api.go:835`).
- For a scoped caller, `/relay/send` also needs `from` bound in the chat its `session_key` names; `from` alone would let one agent drive another project's agent in any chat where that project is bound (`core/relay.go:211-230`).
- The socket is created and checked right after the `run_as_user` preflight, before any engine starts; at `main.go:1261` a new fatal would fire after platforms, cron and timers started (`cmd/cc-connect/main.go:353`, `:1022-1040`, `:1261`).
- The socket folder is resolved through symlinks once, and listen, chmod and the final check all use the resolved path, so a swapped link cannot redirect them (`core/api.go:74-87`).
- A caller whose scope is empty is refused before its request body is read (`core/api.go:229`).
- A `run_as_user` that resolves to uid 0 stops startup; config validation rejects only the names `root` and `0` (`config/config.go:64-78`).
- With no work dir known, a command run as the target starts in the target's home; cc-connect's own cwd from `os.Getwd()` was rejected because sudo would run there as the supervisor first (`core/engine.go:2044-2052`).
- A job a scoped caller added runs as cc-connect's own user once `run_as_user` is removed from its project; recording the creator on the job was rejected as a field with no other caller, and the docs say so (`docs/usage.md:555`).
- The run-as spawn's argv starts with `exec`, so the target's login shell replaces itself with the framed script; dash, the `useradd` default login shell, otherwise keeps the script as a child and sudo's relayed SIGTERM never reaches it (`core/runas.go:180-186`).
- The framed script keeps stdin on fd 3 and starts the command in the background with `<&3` in a new group (`setsid`, else `set -m`); a foreground command would delay the TERM trap, and a background one without the fd copy reads `/dev/null`, breaking `signoff_exec`'s stdin (`core/signoff_exec.go:62`).
- The `0644` stores cc-connect keeps in `data_dir` stay as they are, and the docs say every agent user can read them by name; writing them `0600` under a shared socket was left out because it touches every store's write path beyond the brief's slice. The plan's Deferred / Open Questions keeps it for the author (`core/cron.go:149`, `core/relay.go:400`, `core/workspace_binding.go:211`).

## Plan review

Story ch#2023 and ch#2022 read 2026-11-24; workpackage plan `plans/ch-2022-…/workpackages.gdoc.md` read at `synced_at` 2026-11-24T10:19:07Z (WP11: commands as the agent's user, the socket for its own project only, offer upstream). Every unit traces to a WP11 bullet or a brief case.

- Checked, not blocking: vuyiv's ch#2022 comment (2026-11-24T08:45Z) proposes showing the summary in the preview step with no extra confirmation step, unanswered. The brief (commit 2a9b0227, 10:28Z) and the workpackage plan (synced 10:19Z) are newer and still carry the card (WP5) and WP11, so the plan keeps them.
- Settled: the `0644` stores in `data_dir` ship as a documented limitation; tightening their modes was rejected because no route trusts them and the consumer runs one project per cc-connect (brief "Accepted reach"; `core/cron.go:149`). The plan's Deferred / Open Questions item is closed.
- Cut: startup stops on a `run_as_user` that resolves to uid 0 (KTD7, U9); nothing in the story requires it, and such an agent already runs as root today.
- Cut: U2's custom-exec `$X` test; U1's builder test covers the same quoting, and the site only hands the string on.
- Cut: U4's two-JSON-objects test; the parser is unchanged and its existing test covers it.
- Cut: U4's separate `trap '' TERM` test, merged into the sleep-past-limit test; U1 covers the ignored TERM.
- Cut: U5's "PATH still dropped" test; `core/runas_test.go:62-67` already asserts it.
- Changed: U5 also edits the defaults list at `config.example.toml:240`, and U10 drops `PATH` from the one at `docs/usage.md:472`; both list defaults this unit changes, and neither matches `core/runas.go:99-107`.
- Departure kept: macOS reads peer credentials (KTD5) instead of counting as a platform without them, which the brief would make fatal; macOS `run_as_user` is documented as supported (`docs/usage.md:300`).
- Departure kept: `/relay/send` also needs `from` bound in the chat (KTD13) beyond the brief's `from` check; without it agent A drives B's agent in a chat only B is in (`core/relay.go:211-230` checks only `To`).
- Departure kept: an unknown project and another project's name answer one 403 for a scoped caller (KTD12) instead of today's 404; checking the caller's set first gives that at no cost (`core/api.go:255-261`).
- Departure kept: the workspace clone's stat and mkdir and `/diff`'s `diff2html` run as the target too, beyond the brief's list; the tripwire list forbids root work on a target-writable path.
- Departure kept: the socket is created right after the `run_as_user` preflight (KTD9) instead of at `cmd/cc-connect/main.go:1261`, so its fatals fire before platforms, cron and timers start.

## Build

## Open questions
