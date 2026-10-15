package nodehost

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/artifacts"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: outside retries, so a node stages and publishes once, not per attempt.
func artifactsLayer(cfg *Config, next Executor) Executor {
	workspace := cfg.Session.Dir
	return func(ctx context.Context, n *NodeRun) Result {
		outputs := n.Plan.Artifacts
		edges := make([]artifacts.Edge, 0, len(n.Plan.Consumes))
		for _, c := range n.Plan.Consumes {
			edges = append(edges, artifacts.Edge{Record: blob.RecordKey{Scope: blob.Scope{Job: c.Producer}}, Into: c.Into})
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
		if cfg.Artifacts == nil {
			return artifactFailure("artifacts", errors.New("no authenticated storage binding"))
		}
		data, err := cfg.Artifacts(ctx, n)
		if err != nil {
			return artifactFailure("artifacts", err)
		}
		for i := range edges {
			edges[i].Record.Team = data.Identity.Team
			edges[i].Record.Kind = blob.KindManifest
			edges[i].Record.Name = n.Run.ID
			edges[i].Record.Scope.Repo = data.Authority.Repo
			edges[i].Record.Scope.Ref = data.Authority.Ref.Scope
			edges[i].Record.Scope.Pipeline = data.Pipeline
		}
		if len(edges) > 0 {
			staged, err := artifacts.Stage(ctx, data, workspace, edges)
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
		// safety: application retries advance the upload receipt's attempt identity.
		data, err = cfg.Artifacts(ctx, n)
		if err != nil {
			return artifactFailure("publish artifacts", err)
		}
		warn := func(msg string) { n.Run.Logger.Warn(msg, "run", n.Run.ID, "node", n.Plan.ID) }
		record, err := artifacts.Capture(ctx, data, workspace, outputs, warn)
		if err != nil {
			return artifactFailure("publish artifacts", err)
		}
		if record.Digest != "" {
			hostEventJSON(ctx, n.Run, n.Plan.ID, "artifacts_published", map[string]any{"manifest_digest": record.Digest})
		}
		return res
	}
}

func artifactFailure(what string, err error) Result {
	return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("%s: %w", what, err), Reason: store.FailureUnknown}
}
