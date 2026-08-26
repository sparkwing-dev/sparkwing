package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	localrunner "github.com/sparkwing-dev/sparkwing/internal/runners/local"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
// run.
//
// The state backend decides how a node process reaches run state:
//
//   - *client.Client: a controller already exists; children are told
//     the same URL and token the dispatcher uses.
//   - a local SQLite store: the dispatcher owns the only handle, so it
//     mounts the real controller over it and hands children the URL.
//   - anything else: the dispatcher mounts the node-facing subset of
//     that API over the assembled state backend. Object-store state
//     ("state: {type: s3}") is what reaches this arm today.
//
// The last two are one loopback with two backings, not two execution
// models; the child cannot tell them apart, and nothing local executes
// in the dispatcher's own goroutines any more.
//
// It takes the assembled Backends rather than only opts.State because
// the run's state surface is the one the dispatcher itself writes
// through -- including the local mirror a remote-profile run tees to.
// Pointing children at the raw handle underneath would silently drop
// their writes out of the mirror.
func setupLocalExecution(paths Paths, opts *Options, backends Backends, workDir string, logger *slog.Logger) (*localExecution, error) {
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

	if c, ok := backends.State.(*client.Client); ok {
		cfg.ControllerURL = c.BaseURL()
		cfg.AgentToken = c.Token()
		ctrl = c
		cleanup = func() {}
	} else {
		loopback, lerr := startRunLoopback(opts, backends, logger)
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

// startRunLoopback mounts the controller this run's node processes talk
// to. A run whose state IS a local SQLite store gets the real
// controller over it, which is the richer surface and the one the
// dashboard already shares; every other backing gets the node-facing
// shim over the assembled state backend.
//
// The SQLite arm unwraps to the raw *store.Store because the real
// controller is a database server and the store is what it needs. A
// mirrored run never reaches that arm: its canonical state is remote,
// so its children must write through the tee, not past it.
func startRunLoopback(opts *Options, backends Backends, logger *slog.Logger) (*loopbackController, error) {
	if local, ok := backends.State.(localState); ok {
		return startLoopbackController(local.st, opts.ArtifactStore, opts.RunID, logger)
	}
	return startLoopbackShim(backends.State, backends.Concurrency, opts.ArtifactStore, opts.RunID, logger)
}
