//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

// runRunAsUserStartupChecks runs preflight gates + isolation audit for
// every project that sets run_as_user, and checks the config file at
// configPath. Runs in parallel across projects.
// Fatal on any failure. Must be called BEFORE any engine is constructed.
//
// Returns nil if every project's run_as_user configuration is clean, or
// an aggregate error (with one entry per failing project) otherwise. The
// caller should os.Exit(1) on a non-nil return after logging.
//
// On Windows, this is a no-op because config validation already rejects
// run_as_user at parse time. We still call it so the wiring is in place
// for future platforms.
func runRunAsUserStartupChecks(ctx context.Context, cfg *config.Config, configPath string) error {
	if runtime.GOOS == "windows" {
		return nil
	}

	// Collect projects that have run_as_user set + their work_dirs.
	type pending struct {
		project    string
		runAsUser  string
		workDir    string
		otherUsers []string
	}
	var pendingProjects []pending
	var allUsers []string
	for _, proj := range cfg.Projects {
		if proj.RunAsUser == "" {
			continue
		}
		allUsers = append(allUsers, proj.RunAsUser)
	}
	for _, proj := range cfg.Projects {
		if proj.RunAsUser == "" {
			continue
		}
		wd, _ := proj.Agent.Options["work_dir"].(string)
		pendingProjects = append(pendingProjects, pending{
			project:    proj.Name,
			runAsUser:  proj.RunAsUser,
			workDir:    wd,
			otherUsers: allUsers,
		})
	}
	if len(pendingProjects) == 0 {
		return nil
	}

	slog.Info("run_as_user: running startup safety checks",
		"project_count", len(pendingProjects))

	supervisor := ""
	if u, err := user.Current(); err == nil {
		supervisor = u.Username
	}

	// Fan out preflight + audit per project in parallel. Each project's
	// result is independent; we collect them all before deciding to
	// abort, so a single startup attempt shows every problem.
	type projectOutcome struct {
		project   string
		preflight core.PreflightResult
		audit     core.IsolationReport
		auditErr  error
	}
	outcomes := make([]projectOutcome, len(pendingProjects))
	var wg sync.WaitGroup
	runner := core.ExecSudoRunner{}
	for i, p := range pendingProjects {
		i, p := i, p
		wg.Add(1)
		go func() {
			defer wg.Done()
			pfCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			outcomes[i].project = p.project
			outcomes[i].preflight = core.PreflightRunAsUser(pfCtx, core.PreflightConfig{
				Project:   p.project,
				RunAsUser: p.runAsUser,
				WorkDir:   p.workDir,
				Runner:    runner,
			})
			// Only run the audit probe if preflight passed — otherwise
			// the probe will definitely fail too and the operator only
			// needs to see the preflight error.
			if outcomes[i].preflight.HasFatal() {
				return
			}
			auditCtx, aCancel := context.WithTimeout(ctx, 20*time.Second)
			defer aCancel()
			report, err := core.RunIsolationProbe(auditCtx, core.AuditConfig{
				Project:    p.project,
				RunAsUser:  p.runAsUser,
				WorkDir:    p.workDir,
				OtherUsers: p.otherUsers,
				Supervisor: supervisor,
				Runner:     runner,
			})
			outcomes[i].audit = report
			outcomes[i].auditErr = err
		}()
	}
	wg.Wait()

	// Log every outcome — warnings, fatals, and clean passes — so the
	// operator has a single visible record of what was checked.
	var fatals []error
	if err := checkConfigFile(configPath, cfg.DataDir); err != nil {
		slog.Error("run_as_user: config file FATAL", "error", err)
		fatals = append(fatals, err)
	}
	for _, o := range outcomes {
		for _, w := range o.preflight.Warnings {
			slog.Warn("run_as_user: preflight warning", "project", o.project, "message", w)
		}
		for _, f := range o.preflight.Fatal {
			slog.Error("run_as_user: preflight FATAL", "project", o.project, "error", f)
			fatals = append(fatals, fmt.Errorf("project %q preflight: %w", o.project, f))
		}
		if o.preflight.HasFatal() {
			continue
		}
		if o.auditErr != nil {
			slog.Error("run_as_user: isolation probe failed to run",
				"project", o.project, "error", o.auditErr)
			fatals = append(fatals, fmt.Errorf("project %q probe: %w", o.project, o.auditErr))
			continue
		}
		slog.Info("run_as_user: isolation audit completed",
			"project", o.project,
			"whoami", o.audit.Identity.Whoami,
			"workdir_writable", o.audit.WorkDirStatus.Writable,
			"target_paths", len(o.audit.TargetPaths),
			"cross_user_results", len(o.audit.CrossUser),
		)
		for _, f := range o.audit.Fatal {
			slog.Error("run_as_user: audit FATAL", "project", o.project, "error", f)
			fatals = append(fatals, fmt.Errorf("project %q audit: %s", o.project, f))
		}
	}

	if len(fatals) > 0 {
		return fmt.Errorf("run_as_user startup checks found %d problem(s); see logs above", len(fatals))
	}
	return nil
}

// checkConfigFile returns an error when the config file at configPath is
// inside dataDir and group or others can read it: every run_as_user agent can
// enter dataDir, so it could read the config and the tokens in it.
func checkConfigFile(configPath, dataDir string) error {
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return p
	}
	rel, err := filepath.Rel(resolve(dataDir), resolve(configPath))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fmt.Errorf("check config file %s: %w", configPath, err)
	}
	if info.Mode().Perm()&0o044 == 0 {
		return nil
	}
	return fmt.Errorf("config file %s is inside data_dir %s and readable by other users, so run_as_user agents can read it. Move it outside data_dir, or run chmod 600 %s", configPath, dataDir, configPath)
}

// socketProbe returns the uid of user after checking, as that user, that it
// can reach the API socket at sockPath.
type socketProbe func(ctx context.Context, user, sockPath string) (int, error)

// openAPISocket opens the API socket before any engine starts. Without a
// run_as_user project it is cc-connect's own socket, and a failure only
// warns and returns no server. With one, the socket is shared with the
// projects' users; a platform without peer credentials, a socket that cannot
// be opened safely, or a project user that cannot reach it is an error, and
// each user's uid is handed to the server.
func openAPISocket(ctx context.Context, cfg *config.Config, peerCredentials bool, probe socketProbe) (*core.APIServer, error) {
	var users []string
	for _, proj := range cfg.Projects {
		if proj.RunAsUser != "" && !slices.Contains(users, proj.RunAsUser) {
			users = append(users, proj.RunAsUser)
		}
	}
	if len(users) == 0 {
		srv, err := core.NewAPIServer(cfg.DataDir, false)
		if err != nil {
			slog.Warn("api server unavailable", "error", err)
			return nil, nil
		}
		return srv, nil
	}
	if !peerCredentials {
		return nil, fmt.Errorf("run_as_user needs the API socket to read each caller's uid, which cc-connect cannot do on %s; remove run_as_user or run on Linux or macOS", runtime.GOOS)
	}
	srv, err := core.NewAPIServer(cfg.DataDir, true)
	if err != nil {
		return nil, fmt.Errorf("open the API socket for run_as_user: %w", err)
	}
	ids := make(map[string]int, len(users))
	var errs []error
	for _, user := range users {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		uid, err := probe(probeCtx, user, srv.SocketPath())
		cancel()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ids[user] = uid
	}
	if err := errors.Join(errs...); err != nil {
		srv.Stop()
		return nil, err
	}
	srv.SetRunAsUserIDs(ids)
	return srv, nil
}
