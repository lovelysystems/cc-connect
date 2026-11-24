//go:build windows

package main

import (
	"context"
	"log/slog"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func runRunAsUserStartupChecks(_ context.Context, _ *config.Config) error {
	return nil
}

type socketProbe func(ctx context.Context, user, sockPath string) (int, error)

// openAPISocket opens cc-connect's own API socket; run_as_user is not
// supported on Windows. A failure only warns and returns no server.
func openAPISocket(_ context.Context, cfg *config.Config, _ bool, _ socketProbe) (*core.APIServer, error) {
	srv, err := core.NewAPIServer(cfg.DataDir, false)
	if err != nil {
		slog.Warn("api server unavailable", "error", err)
		return nil, nil
	}
	return srv, nil
}
