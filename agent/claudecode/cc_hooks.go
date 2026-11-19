package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Under run_as_user, settings are read and hooks run through sudo -i, which
// joins argv into one string for the target's login shell. The scripts below
// therefore stay on one line, read variables only as "${NAME}", and frame
// what they print between marker lines so login-profile output is ignored.
const (
	settingsBeginMarker = "@@cc-connect-settings-begin"
	settingsEndMarker   = "@@cc-connect-settings-end"
	hookBeginMarker     = "@@cc-connect-hook-begin"
	hookEndMarker       = "@@cc-connect-hook-end"
	hookWorkDirEnv      = "CC_RUNAS_HOOK_WORKDIR"
	hookCommandEnv      = "CC_RUNAS_HOOK_COMMAND"
)

// hookAsUserScript runs the hook command from hookCommandEnv with sh -c and
// keeps its exit status.
const hookAsUserScript = `printf '%s\n' '` + hookBeginMarker + `'; ` +
	`sh -c "${` + hookCommandEnv + `}"; s=$?; ` +
	`printf '\n%s\n' '` + hookEndMarker + `'; exit "${s}"`

// settingsAsUserScript prints each existing settings file from the target's
// Claude config home and, when withWorkDir is set, from the work dir in
// hookWorkDirEnv, in the order settingsPaths uses.
func settingsAsUserScript(withWorkDir bool) string {
	files := `"${c}/settings.json" "${c}/settings.local.json"`
	if withWorkDir {
		files += ` "${` + hookWorkDirEnv + `}/.claude/settings.json" "${` + hookWorkDirEnv + `}/.claude/settings.local.json"`
	}
	return `c="${CLAUDE_CONFIG_DIR:-${HOME}/.claude}"; for f in ` + files + `; do ` +
		`if [ -f "${f}" ]; then printf '\n%s\n' "` + settingsBeginMarker + ` ${f}"; cat -- "${f}"; ` +
		`printf '\n%s\n' '` + settingsEndMarker + `'; fi; done`
}

// ccPermissionHookEntry represents one PermissionRequest hook entry from
// Claude Code's settings.json.
type ccPermissionHookEntry struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

// ccHooksSection is the "hooks" section of Claude Code settings.json.
type ccHooksSection struct {
	PermissionRequest []ccPermissionHookEntry `json:"PermissionRequest"`
}

// ccSettings is the relevant subset of Claude Code settings.json.
type ccSettings struct {
	Hooks ccHooksSection `json:"hooks"`
}

// ccHookDecision is the parsed decision from a hook's stdout.
type ccHookDecision struct {
	Behavior string // "allow", "deny", or "" (ask/fallthrough)
	Message  string // reason for deny (optional)
}

// hookContext carries the session context needed to build hook stdin.
// Fields mirror Claude Code's PermissionRequest hook input spec.
type hookContext struct {
	sessionID          string
	toolName           string
	toolInput          map[string]any
	cwd                string
	permissionMode     string
	transcriptPath     string
	permissionSuggestions []any // nil = omit from stdin
	agentID            string // empty = omit
	agentType          string // empty = omit
}

// ccPermissionHookRunner reads and executes Claude Code PermissionRequest
// hooks. Settings are loaded once on first use and reused for the session.
type ccPermissionHookRunner struct {
	workDir   string
	spawnOpts core.SpawnOptions
	once      sync.Once
	settings  *ccSettings
}

func newCCPermissionHookRunner(workDir string, spawnOpts core.SpawnOptions) *ccPermissionHookRunner {
	return &ccPermissionHookRunner{
		workDir:   workDir,
		spawnOpts: spawnOpts,
	}
}

// tryHook finds and executes a matching PermissionRequest hook.
// Returns (decision, true) if a hook matched and produced allow/deny.
// Returns (_, false) if no hook matched, hook returned "ask", or any error.
func (r *ccPermissionHookRunner) tryHook(
	ctx context.Context,
	hctx hookContext,
) (ccHookDecision, bool) {
	r.once.Do(func() {
		if r.spawnOpts.IsolationMode() {
			r.settings = r.loadSettingsAsUser(ctx)
		} else {
			r.settings = r.loadSettings()
		}
	})
	if r.settings == nil {
		return ccHookDecision{}, false
	}
	settings := r.settings

	for _, entry := range settings.Hooks.PermissionRequest {
		if !matchHookEntry(entry.Matcher, hctx.toolName) {
			continue
		}
		for _, h := range entry.Hooks {
			if h.Type != "command" || h.Command == "" {
				continue
			}
			stdinData := buildHookStdin(hctx)
			var decision ccHookDecision
			var err error
			if r.spawnOpts.IsolationMode() {
				decision, err = runHookCommandAsUser(ctx, r.spawnOpts, h.Command, stdinData)
			} else {
				decision, err = runHookCommand(ctx, h.Command, stdinData)
			}
			if err != nil {
				slog.Warn("ccHooks: hook command failed",
					"command", truncateStr(h.Command, 80), "error", err)
				continue
			}
			if decision.Behavior == "allow" || decision.Behavior == "deny" {
				return decision, true
			}
			// "ask" or empty — fall through to next hook / platform
		}
	}
	return ccHookDecision{}, false
}

// loadSettings reads and merges settings.json files. Called once per session.
func (r *ccPermissionHookRunner) loadSettings() *ccSettings {
	merged := &ccSettings{}
	found := false
	for _, p := range settingsPaths(r.workDir) {
		s, err := readSettingsFile(p)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Debug("ccHooks: skipping settings file", "path", p, "error", err)
			}
			continue
		}
		merged.Hooks.PermissionRequest = append(merged.Hooks.PermissionRequest, s.Hooks.PermissionRequest...)
		found = true
	}
	if !found {
		slog.Debug("ccHooks: no settings files found")
		return nil
	}
	return merged
}

// loadSettingsAsUser reads and merges the settings files as the run_as_user
// user, in one spawn. Any failure leaves the session without hooks.
func (r *ccPermissionHookRunner) loadSettingsAsUser(ctx context.Context) *ccSettings {
	out, err := r.readSettingsAsUser(ctx)
	if err != nil {
		slog.Warn("ccHooks: reading settings as run_as_user failed", "user", r.spawnOpts.RunAsUser, "error", err)
		return nil
	}
	merged := &ccSettings{}
	found := false
	for _, f := range parseFramedSettings(out) {
		s, err := parseSettings(f.path, f.data)
		if err != nil {
			slog.Debug("ccHooks: skipping settings file", "path", f.path, "error", err)
			continue
		}
		merged.Hooks.PermissionRequest = append(merged.Hooks.PermissionRequest, s.Hooks.PermissionRequest...)
		found = true
	}
	if !found {
		slog.Debug("ccHooks: no settings files found", "user", r.spawnOpts.RunAsUser)
		return nil
	}
	return merged
}

func (r *ccPermissionHookRunner) readSettingsAsUser(ctx context.Context) ([]byte, error) {
	if err := verifyRunAsUser(ctx, r.spawnOpts.RunAsUser); err != nil {
		return nil, fmt.Errorf("run_as_user spawn refused: %w", err)
	}

	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	opts := r.spawnOpts
	opts.WorkDir = ""
	opts.EnvAllowlist = append(append([]string(nil), opts.EnvAllowlist...), hookWorkDirEnv)
	env := os.Environ()
	if r.workDir != "" {
		absWorkDir, err := filepath.Abs(r.workDir)
		if err != nil {
			return nil, fmt.Errorf("resolve work dir: %w", err)
		}
		env = core.MergeEnv(env, []string{hookWorkDirEnv + "=" + absWorkDir})
	}
	cmd := core.BuildSpawnCommand(readCtx, opts, "/bin/sh", "-c", settingsAsUserScript(r.workDir != ""))
	// sudo relays SIGTERM but not SIGKILL. WaitDelay ends Wait when a child
	// still holds stdout after sudo is gone.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = core.FilterEnvForSpawn(env, opts)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read settings: %w (stderr: %s)", err, truncateStr(strings.TrimSpace(stderr.String()), 200))
	}
	return stdout.Bytes(), nil
}

type framedSettingsFile struct {
	path string
	data []byte
}

// parseFramedSettings returns the files settingsAsUserScript printed, in
// order, ignoring every line outside a frame.
func parseFramedSettings(out []byte) []framedSettingsFile {
	var files []framedSettingsFile
	var cur *framedSettingsFile
	var body []string
	for _, line := range strings.Split(string(out), "\n") {
		if cur == nil {
			if path, ok := strings.CutPrefix(line, settingsBeginMarker+" "); ok {
				cur = &framedSettingsFile{path: path}
				body = nil
			}
			continue
		}
		if line == settingsEndMarker {
			cur.data = []byte(strings.Join(body, "\n"))
			files = append(files, *cur)
			cur = nil
			continue
		}
		body = append(body, line)
	}
	return files
}

// settingsPaths returns the settings.json paths to read, in order.
func settingsPaths(workDir string) []string {
	configHome := claudeConfigHomeDir()
	paths := []string{
		filepath.Join(configHome, "settings.json"),
		filepath.Join(configHome, "settings.local.json"),
	}
	if workDir != "" {
		paths = append(paths,
			filepath.Join(workDir, ".claude", "settings.json"),
			filepath.Join(workDir, ".claude", "settings.local.json"),
		)
	}
	return paths
}

// readSettingsFile reads a single settings.json, stripping JSONC comments.
func readSettingsFile(path string) (*ccSettings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSettings(path, data)
}

// parseSettings parses the contents of one settings.json, stripping JSONC
// comments.
func parseSettings(path string, data []byte) (*ccSettings, error) {
	cleaned := stripJSONC(data)
	var s ccSettings
	if err := json.Unmarshal(cleaned, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, nil
}

// stripJSONC removes // and /* */ comments from JSONC, preserving strings.
func stripJSONC(data []byte) []byte {
	var out bytes.Buffer
	inString := false
	escaped := false
	i := 0
	for i < len(data) {
		ch := data[i]

		if escaped {
			out.WriteByte(ch)
			escaped = false
			i++
			continue
		}

		if inString {
			out.WriteByte(ch)
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			i++
			continue
		}

		// Not in string.
		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			i++
			continue
		}

		if ch == '/' && i+1 < len(data) {
			next := data[i+1]
			if next == '/' {
				// Line comment — skip to end of line.
				i += 2
				for i < len(data) && data[i] != '\n' {
					i++
				}
				continue
			}
			if next == '*' {
				// Block comment — skip to closing */.
				i += 2
				closed := false
				for i+1 < len(data) {
					if data[i] == '*' && data[i+1] == '/' {
						i += 2
						closed = true
						break
					}
					i++
				}
				if !closed {
					return data // return original; json.Unmarshal will give a clear error
				}
				continue
			}
		}

		out.WriteByte(ch)
		i++
	}
	return out.Bytes()
}

// matchHookEntry checks if toolName matches the matcher.
// Empty or "*" matcher matches everything. Otherwise exact match.
func matchHookEntry(matcher, toolName string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	return strings.EqualFold(matcher, toolName)
}

// runHookCommand executes a hook command with tool info on stdin.
// Returns the parsed decision. Timeout: 60s (matching Claude Code's own).
func runHookCommand(
	ctx context.Context,
	command string,
	stdinData map[string]any,
) (ccHookDecision, error) {
	stdinJSON, err := json.Marshal(stdinData)
	if err != nil {
		return ccHookDecision{}, fmt.Errorf("marshal stdin: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdinJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Strip the skip flag so the hook does real work when cc-connect
	// calls it (even if the host environment has it set).
	cmd.Env = filterEnv(os.Environ(), "CC_CONNECT_PERMISSION_HOOK_SKIP")

	if err := cmd.Run(); err != nil {
		return ccHookDecision{}, fmt.Errorf("hook exec: %w (stderr: %s)", err, truncateStr(strings.TrimSpace(stderr.String()), 200))
	}

	return parseHookOutput(stdout.Bytes())
}

// runHookCommandAsUser runs a hook command as the run_as_user user through
// sudo, with tool info on stdin.
func runHookCommandAsUser(
	ctx context.Context,
	opts core.SpawnOptions,
	command string,
	stdinData map[string]any,
) (ccHookDecision, error) {
	stdinJSON, err := json.Marshal(stdinData)
	if err != nil {
		return ccHookDecision{}, fmt.Errorf("marshal stdin: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := verifyRunAsUser(timeoutCtx, opts.RunAsUser); err != nil {
		return ccHookDecision{}, fmt.Errorf("run_as_user spawn refused: %w", err)
	}

	opts.WorkDir = ""
	opts.EnvAllowlist = append(append([]string(nil), opts.EnvAllowlist...), hookCommandEnv)
	cmd := core.BuildSpawnCommand(timeoutCtx, opts, "/bin/sh", "-c", hookAsUserScript)
	// sudo relays SIGTERM to the hook but cannot relay the SIGKILL
	// CommandContext sends by default. WaitDelay stops Wait from blocking on
	// a child that still holds stdout after sudo is gone.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(stdinJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	env := filterEnv(os.Environ(), "CC_CONNECT_PERMISSION_HOOK_SKIP")
	cmd.Env = core.FilterEnvForSpawn(core.MergeEnv(env, []string{hookCommandEnv + "=" + command}), opts)

	if err := cmd.Run(); err != nil {
		return ccHookDecision{}, fmt.Errorf("hook exec as %q: %w (stderr: %s)", opts.RunAsUser, err, truncateStr(strings.TrimSpace(stderr.String()), 200))
	}

	out, ok := framedHookOutput(stdout.Bytes())
	if !ok {
		return ccHookDecision{}, fmt.Errorf("hook exec as %q: no framed output", opts.RunAsUser)
	}
	return parseHookOutput(out)
}

// framedHookOutput returns what the hook printed between the markers
// hookAsUserScript writes.
func framedHookOutput(out []byte) ([]byte, bool) {
	begin := []byte(hookBeginMarker + "\n")
	end := []byte("\n" + hookEndMarker)
	start := bytes.Index(out, begin)
	if start < 0 {
		return nil, false
	}
	rest := out[start+len(begin):]
	stop := bytes.LastIndex(rest, end)
	if stop < 0 {
		return nil, false
	}
	return rest[:stop], true
}

// parseHookOutput parses hook stdout into a decision.
func parseHookOutput(data []byte) (ccHookDecision, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ccHookDecision{}, nil // empty = ask/fallthrough
	}

	// Try plain text first: "allow", "deny", "ask".
	text := strings.ToLower(string(trimmed))
	switch text {
	case "allow":
		return ccHookDecision{Behavior: "allow"}, nil
	case "deny":
		return ccHookDecision{Behavior: "deny"}, nil
	case "ask":
		return ccHookDecision{}, nil
	}

	// Try structured JSON output.
	var out struct {
		HookSpecificOutput struct {
			Decision struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return ccHookDecision{}, fmt.Errorf("parse hook output: %w", err)
	}
	behavior := strings.ToLower(out.HookSpecificOutput.Decision.Behavior)
	if behavior == "allow" || behavior == "deny" {
		return ccHookDecision{
			Behavior: behavior,
			Message:  out.HookSpecificOutput.Decision.Message,
		}, nil
	}
	return ccHookDecision{}, nil
}

// buildHookStdin constructs the JSON payload for the hook's stdin,
// matching Claude Code's PermissionRequest hook input spec.
func buildHookStdin(hctx hookContext) map[string]any {
	m := map[string]any{
		"session_id":      hctx.sessionID,
		"hook_event_name": "PermissionRequest",
		"tool_name":       hctx.toolName,
		"tool_input":      hctx.toolInput,
	}
	if hctx.cwd != "" {
		m["cwd"] = hctx.cwd
	}
	if hctx.permissionMode != "" {
		m["permission_mode"] = hctx.permissionMode
	}
	if hctx.transcriptPath != "" {
		m["transcript_path"] = hctx.transcriptPath
	}
	if hctx.permissionSuggestions != nil {
		m["permission_suggestions"] = hctx.permissionSuggestions
	}
	if hctx.agentID != "" {
		m["agent_id"] = hctx.agentID
	}
	if hctx.agentType != "" {
		m["agent_type"] = hctx.agentType
	}
	return m
}

// truncateStr truncates s to maxLen characters, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
