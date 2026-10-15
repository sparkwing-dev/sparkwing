package nodehost

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: outside retries, so a node stages and publishes once, not per attempt.
func artifactsLayer(cfg *Config, next Executor) Executor {
	art, workspace := cfg.Artifacts, cfg.Session.Dir
	if art == nil {
		return next
	}
	return func(ctx context.Context, n *NodeRun) Result {
		outputs := n.Plan.Artifacts
		edges := make([]artifacts.Edge, 0, len(n.Plan.Consumes))
		for _, c := range n.Plan.Consumes {
			edges = append(edges, artifacts.Edge{Producer: c.Producer, Into: c.Into})
		}
		if len(outputs) == 0 && len(edges) == 0 {
			return next(ctx, n)
		}
		if err := artifacts.CheckPaths(outputs, edges); err != nil {
			return artifactFailure("declared artifact paths", err)
		}
		if workspace == "" {
			return artifactFailure("artifacts", errors.New("no workspace directory to stage into or publish from"))
		}
		if len(edges) > 0 {
			staged, err := artifacts.Stage(ctx, art, n.Run.State, n.Run.ID, workspace, edges)
			if err != nil {
				return artifactFailure("stage consumed artifacts", err)
			}
			if staged > 0 {
				hostEventJSON(ctx, n.Run, n.Plan.ID, "artifacts_staged", map[string]any{"files": staged})
			}
		}
		res := next(ctx, n)
		if res.Outcome != sparkwing.Success || len(outputs) == 0 {
			return res
		}
		warn := func(msg string) { n.Run.Logger.Warn(msg, "run", n.Run.ID, "node", n.Plan.ID) }
		digest, err := artifacts.Capture(ctx, art, workspace, outputs, warn)
		if err == nil {
			// safety: an unrecorded manifest would leave consumers staging nothing
			// without error, so failing to record it fails the producer.
			err = n.Run.State.SetNodeArtifactManifest(context.WithoutCancel(ctx), n.Run.ID, n.Plan.ID, digest)
		}
		if err != nil {
			return artifactFailure("publish artifacts", err)
		}
		if digest != "" {
			hostEventJSON(ctx, n.Run, n.Plan.ID, "artifacts_published", map[string]any{"manifest_digest": digest})
		}
		return res
	}
}

func artifactFailure(what string, err error) Result {
	return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("%s: %w", what, err), Reason: store.FailureUnknown}
}
