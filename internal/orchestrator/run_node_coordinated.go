package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// runNodeConfig is the resolved option set for one RunNodeOnce call.
type runNodeConfig struct {
	coordinated bool
}

// RunNodeOption selects a RunNodeOnce execution mode.
type RunNodeOption func(*runNodeConfig)

// Coordinated declares that a local dispatcher owns this node's
// coordination and has already resolved it: the cache lookup, the
// concurrency slot, and the SkipIf predicates all ran before the
// dispatcher decided to spawn this process at all. Re-running them
// here would take a second slot against the same budget, re-evaluate a
// predicate whose answer the dispatcher already acted on, and turn a
// cache miss the dispatcher observed into a second store round trip.
//
// Without it RunNodeOnce keeps the pod contract, where nothing
// upstream resolved anything.
func Coordinated() RunNodeOption {
	return func(c *runNodeConfig) { c.coordinated = true }
}

// executeCoordinated runs the node body directly, skipping the
// coordination the dispatcher already did. Terminal state is written
// by the execution path itself, exactly as on the in-process path;
// the returned Result is what the parent reads if it can.
func (r *InProcessRunner) executeCoordinated(ctx context.Context, req runner.Request) runner.Result {
	node := req.Node
	if node == nil {
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("run-node --coordinated: Request.Node is nil for %s/%s", req.RunID, req.NodeID),
		}
	}
	output, err := r.executeNode(ctx, req.RunID, node, req.Delegate)
	if err != nil {
		nodeID := req.NodeID
		if nodeID == "" {
			nodeID = node.ID()
		}
		r.markFailedIfUnfinished(ctx, req.RunID, nodeID, err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	return runner.Result{Outcome: sparkwing.Success, Output: output}
}

// installStepControlsFromEnv puts the run's step-range and dry-run
// selections onto ctx from the environment the dispatcher stamped.
// These are run-level decisions made before any node was created, so a
// node executing in its own process has to be told: without them a
// --dry-run run applies for real from the first spawned node, and a
// --start-at window silently covers nothing.
//
// Only the coordinated path reads them, and the gate is not
// cosmetic. A pod or a warm-pool runner inherits an ambient
// environment nobody set for this run -- the pool worker takes the
// admission daemon's shell env -- so an operator who once exported
// SPARKWING_DRY_RUN would silently dry-run every node the pool
// executed, and a stale SPARKWING_START_AT would hard-fail them. A
// spawned node's environment, by contrast, is the one childEnv
// wrote.
func installStepControlsFromEnv(ctx context.Context, plan *sparkwing.Plan) (context.Context, error) {
	startAt := os.Getenv("SPARKWING_START_AT")
	stopAt := os.Getenv("SPARKWING_STOP_AT")
	if startAt != "" || stopAt != "" {
		if err := sparkwingruntime.ValidateStepRange(plan, startAt, stopAt); err != nil {
			return ctx, err
		}
		ctx = sparkwingruntime.WithStepRange(ctx, startAt, stopAt)
	}
	if os.Getenv("SPARKWING_DRY_RUN") == "1" {
		ctx = sparkwingruntime.WithDryRun(ctx)
	}
	return ctx, nil
}

// coordinatedChildSurfaces rebuilds the secrets source and artifact
// store a locally-dispatched node needs, from the same profile
// resolution the dispatcher ran.
//
// Every local secrets backend is bound to a file or the environment
// rather than to the dispatcher's memory (dotenv, filesystem, env), so
// re-resolving it here reaches the same values. A controller-backed
// profile resolves the same way it does for the dispatcher.
func coordinatedChildSurfaces(ctx context.Context, pipeline string) (secrets.Source, storage.ArtifactStore, error) {
	projectCfg := bindProjectPipelines()
	prof, _, err := resolveActiveProfile(loadPipelineYAML(pipeline), projectCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("run-node --coordinated: resolve profile: %w", err)
	}

	art, err := coordinatedArtifactStore(ctx, prof)
	if err != nil {
		return nil, nil, err
	}

	source, err := selectSecretResolver(ctx, Options{Profile: prof})
	if err != nil {
		return nil, art, fmt.Errorf("run-node --coordinated: secrets backend: %w", err)
	}
	if source == nil {
		// safety: RunLocal's own default when the profile declares no secrets
		// surface. The child has to make the same choice or a laptop
		// pipeline's Secret() calls start failing the moment its nodes
		// move out of the dispatcher's process.
		source = secrets.NewDotenvSource("")
	}
	return source, art, nil
}

// coordinatedArtifactStore opens the store a locally dispatched node
// publishes outputs to and stages consumed inputs from. Precedence,
// highest first:
//
//  1. the run profile's cache surface -- what the dispatcher itself
//     resolved, so the node writes where the run's manifest is read;
//  2. an explicit SPARKWING_CACHE_URL in the environment, which the
//     dispatcher pinned from its own for exactly this purpose;
//  3. nothing, which leaves the node without artifacts.
//
// $SPARKWING_HOME/dev.env is deliberately not consulted, for the same
// reason the child is passed --logs= empty: a resident dashboard
// writes its own URLs into that file, and a run whose profile names
// S3 would otherwise stage from the dashboard's local cache while
// recording a manifest in the bucket. The two halves of a node's
// artifacts must not come from different stores.
func coordinatedArtifactStore(ctx context.Context, prof *profile.Profile) (storage.ArtifactStore, error) {
	if prof != nil {
		if _, _, cache := profileSurfaceSpecs(prof, ""); cache != nil {
			art, err := storeurl.OpenArtifactStoreFromSpec(ctx, *cache, profileControllerLookup(prof))
			if err != nil {
				return nil, fmt.Errorf("run-node --coordinated: cache backend: %w", err)
			}
			return art, nil
		}
	}
	url := strings.TrimSpace(os.Getenv(ArtifactStoreEnvVar))
	if url == "" {
		return nil, nil
	}
	art, err := storeurl.OpenArtifactStore(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("run-node --coordinated: cache backend %s: %w", url, err)
	}
	return art, nil
}
