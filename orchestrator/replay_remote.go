package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// SideloadRemoteForReplay copies a remote run's state into the local
// store so MintReplayRun + replay-node can run as if it were local.
// Copies the run row, target node + upstream dep rows (with output),
// and the target's dispatch snapshot. Idempotent.
func SideloadRemoteForReplay(ctx context.Context, st *store.Store, c *client.Client, runID, nodeID string) error {
	if err := sideloadRun(ctx, st, c, runID); err != nil {
		return err
	}
	targetNode, err := sideloadNode(ctx, st, c, runID, nodeID)
	if err != nil {
		return fmt.Errorf("sideload target node %s: %w", nodeID, err)
	}
	for _, dep := range targetNode.Deps {
		if _, err := sideloadNode(ctx, st, c, runID, dep); err != nil {
			return fmt.Errorf("sideload dep %s: %w", dep, err)
		}
	}
	if err := sideloadDispatch(ctx, st, c, runID, nodeID); err != nil {
		return err
	}
	return nil
}

func sideloadRun(ctx context.Context, st *store.Store, c *client.Client, runID string) error {
	if existing, err := st.GetRun(ctx, runID); err == nil && existing != nil {
		return nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("check local run: %w", err)
	}
	remote, err := c.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("fetch remote run %s: %w", runID, err)
	}
	if err := st.CreateRun(ctx, *remote); err != nil {
		return fmt.Errorf("sideload run %s: %w", runID, err)
	}
	if remote.Status != "running" && remote.Status != "" {
		// Stamp finished_at so list/status views don't show this as
		// forever-running locally.
		_ = st.FinishRun(ctx, remote.ID, remote.Status, remote.Error)
	}
	return nil
}

func sideloadNode(ctx context.Context, st *store.Store, c *client.Client, runID, nodeID string) (*store.Node, error) {
	if existing, err := st.GetNode(ctx, runID, nodeID); err == nil && existing != nil {
		return existing, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("check local node: %w", err)
	}
	remote, err := c.GetNode(ctx, runID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("fetch remote node: %w", err)
	}
	if err := st.CreateNode(ctx, *remote); err != nil {
		return nil, fmt.Errorf("sideload node row: %w", err)
	}
	// CreateNode writes pending fields; only terminalize when there's
	// real output (resolver fallback needs Output bytes).
	if remote.Status == "done" || remote.Outcome != "" || len(remote.Output) > 0 {
		outcome := remote.Outcome
		if outcome == "" {
			outcome = "success"
		}
		if err := st.FinishNode(ctx, runID, nodeID, outcome, remote.Error, remote.Output); err != nil {
			return nil, fmt.Errorf("finalize sideloaded node: %w", err)
		}
	}
	return remote, nil
}

func sideloadDispatch(ctx context.Context, st *store.Store, c *client.Client, runID, nodeID string) error {
	if existing, err := st.GetNodeDispatch(ctx, runID, nodeID, -1); err == nil && existing != nil {
		return nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("check local dispatch: %w", err)
	}
	remote, err := c.GetNodeDispatch(ctx, runID, nodeID, -1)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("no dispatch snapshot for %s/%s on remote -- "+
				"the original run may predate the dispatch-snapshot feature, "+
				"or the runner that executed this node hasn't been rolled out yet",
				runID, nodeID)
		}
		return fmt.Errorf("fetch remote dispatch: %w", err)
	}
	if err := st.WriteNodeDispatch(ctx, *remote); err != nil {
		return fmt.Errorf("sideload dispatch: %w", err)
	}
	return nil
}
