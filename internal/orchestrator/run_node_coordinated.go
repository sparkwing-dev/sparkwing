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

type runNodeConfig struct {
	coordinated bool
}

type RunNodeOption func(*runNodeConfig)

func Coordinated() RunNodeOption {
	return func(c *runNodeConfig) { c.coordinated = true }
}

func (r *NodeExecutor) executeCoordinated(ctx context.Context, req runner.Request) runner.Result {
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

func coordinatedChildSurfaces(ctx context.Context, pipeline string) (secrets.Source, storage.ArtifactStore, LogBackend, error) {
	projectCfg := bindProjectPipelines()
	prof, _, err := resolveActiveProfile(loadPipelineYAML(pipeline), projectCfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("run-node --coordinated: resolve profile: %w", err)
	}

	art, err := coordinatedArtifactStore(ctx, prof)
	if err != nil {
		return nil, nil, nil, err
	}

	logs, err := coordinatedLogBackend(ctx, prof)
	if err != nil {
		return nil, art, nil, err
	}

	source, err := selectSecretResolver(ctx, Options{Profile: prof})
	if err != nil {
		return nil, art, logs, fmt.Errorf("run-node --coordinated: secrets backend: %w", err)
	}
	if source == nil {
		// safety: RunLocal's own default when the profile declares no secrets
		// surface. The child has to make the same choice or a laptop
		// pipeline's Secret() calls start failing the moment its nodes
		// move out of the dispatcher's process.
		source = secrets.NewDotenvSource("")
	}
	return source, art, logs, nil
}

func coordinatedLogBackend(ctx context.Context, prof *profile.Profile) (LogBackend, error) {
	if prof == nil {
		return nil, nil
	}
	_, logs, _ := profileSurfaceSpecs(prof, "")
	if logs == nil {
		return nil, nil
	}
	sink, err := storeurl.OpenLogStoreFromSpec(ctx, *logs, profileControllerLookup(prof))
	if err != nil {
		return nil, fmt.Errorf(
			"run-node --coordinated: profile %q declares a %s logs surface this node cannot open: %w",
			prof.Name, logs.Type, err)
	}
	return NewLogStoreBackend(sink, nil), nil
}

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
