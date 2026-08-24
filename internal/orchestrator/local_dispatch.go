package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	localrunner "github.com/sparkwing-dev/sparkwing/internal/runners/local"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// selfExecutable is the binary a node process re-enters.
//
// It is resolved once, at first use, and never again. The orchestrator
// IS the compiled pipeline binary, and under `go run` (or
// SPARKWING_NO_BINCACHE) that binary is a temporary file: reading its
// path early and holding it means a later cleanup of the temporary
// directory cannot strand a spawn minutes into a run.
var selfExecutable = sync.OnceValues(os.Executable)

// localExecution is the node-execution wiring for one local run,
// together with whatever it has to tear down.
type localExecution struct {
	runner  *localrunner.Runner
	cleanup func()
}

// setupLocalExecution builds the process-per-node runner for a local
// run, or reports (nil, nil) when this run's state backend cannot
// serve node processes and execution stays in the dispatcher's own
// process.
//
// The state backend decides how a node process reaches run state:
//
//   - *store.Store: the dispatcher owns the only SQLite handle, so it
//     mounts a loopback controller over it and hands children the URL.
//   - *client.Client: a controller already exists; children are told
//     the same URL and token the dispatcher uses.
//   - anything else (S3-only state): there is no controller to point a
//     child at, and RunNodeOnce is written against one. Such a run
//     keeps executing in-process.
func setupLocalExecution(paths Paths, opts *Options, workDir string, logger *slog.Logger) (*localExecution, error) {
	exe, err := selfExecutable()
	if err != nil {
		return nil, fmt.Errorf("resolve pipeline binary for node processes: %w", err)
	}

	cfg := localrunner.Config{
		Executable: exe,
		WorkDir:    workDir,
		Home:       paths.Root,
		// safety: read from the process environment, never through
		// ResolveDevEnvURL. A resident dashboard writes its own cache URL
		// into $SPARKWING_HOME/dev.env, and pinning that would send node
		// artifacts to a store this run's profile never named. The child
		// prefers the profile's cache surface over this value anyway; it
		// carries the operator's explicit export for the case where the
		// profile declares no cache at all.
		CacheURL: os.Getenv(ArtifactStoreEnvVar),
		DryRun:   opts.DryRun,
		StartAt:  opts.StartAt,
		StopAt:   opts.StopAt,
		Labels:   defaultLocalLabels(),
		Logger:   logger,
	}

	var ctrl *client.Client
	var cleanup func()

	switch s := opts.State.(type) {
	case *store.Store:
		loopback, lerr := startLoopbackController(s, opts.ArtifactStore, opts.RunID, logger)
		if lerr != nil {
			return nil, lerr
		}
		cfg.ControllerURL = loopback.url
		cfg.AgentToken = loopback.token
		ctrl = client.NewWithToken(loopback.url, nil, loopback.token)
		cleanup = loopback.Close
	case *client.Client:
		cfg.ControllerURL = s.BaseURL()
		cfg.AgentToken = s.Token()
		ctrl = s
		cleanup = func() {}
	case *s3state.Backend:
		return nil, nil
	default:
		return nil, nil
	}

	return &localExecution{runner: localrunner.New(ctrl, cfg), cleanup: cleanup}, nil
}
