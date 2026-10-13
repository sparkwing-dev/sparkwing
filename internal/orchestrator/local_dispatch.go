package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	localrunner "github.com/sparkwing-dev/sparkwing/internal/runners/local"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

var selfExecutable = sync.OnceValues(os.Executable)

type localExecution struct {
	runner  *localrunner.Runner
	cleanup func()
}

func setupLocalExecution(ctx context.Context, paths Paths, opts *Options, backends Backends, workDir string, logger *slog.Logger) (*localExecution, error) {
	exe, err := selfExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve pipeline binary for node processes: %w", err)
	}

	cfg := localrunner.Config{
		Executable: exe,
		WorkDir:    workDir,
		Home:       paths.Root,
		// safety: read only the process environment; the dashboard's dev.env
		// cache URL is not part of this run's selected profile.
		CacheURL: os.Getenv(ArtifactStoreEnvVar),
		DryRun:   opts.DryRun,
		StartAt:  opts.StartAt,
		StopAt:   opts.StopAt,
		Labels:   defaultLocalLabels(),
		Logger:   logger,
	}

	var ctrl *client.Client
	var cleanup func()

	if c, ok := backends.State.(*client.Client); ok {
		cfg.ControllerURL = c.BaseURL()
		cfg.AgentToken = c.Token()
		cfg.APISocket = backends.APISocket
		ctrl = c
		cleanup = func() {}
	} else {
		local, ok := backends.State.(localState)
		if !ok {
			return nil, fmt.Errorf("local execution: state backend %T is neither this machine's store nor a controller", backends.State)
		}
		loopback, lerr := startLoopbackController(ctx, local.st, opts.ArtifactStore, opts.RunID, logger)
		if lerr != nil {
			return nil, lerr
		}
		cfg.ControllerURL = loopback.url
		cfg.AgentToken = loopback.token
		ctrl = client.NewWithToken(loopback.url, nil, loopback.token)
		cleanup = loopback.Close
	}

	return &localExecution{runner: localrunner.New(ctrl, cfg), cleanup: cleanup}, nil
}
