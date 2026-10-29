package teststore

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// Node returns one node of the tenant's run through ListNodes, or store.ErrNotFound.
func Node(ctx context.Context, tenant *store.Tenant, runID, nodeID string) (*store.Node, error) {
	nodes, err := tenant.ListNodes(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.NodeID == nodeID {
			return n, nil
		}
	}
	return nil, store.ErrNotFound
}

// SecretRow returns the tenant's stored row with exactly this name, repository
// and pipeline, or store.ErrNotFound.
func SecretRow(tenant *store.Tenant, name, repo, pipeline string) (*store.Secret, error) {
	rows, err := tenant.ListSecrets()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Name == name && rows[i].Repo == repo && rows[i].Pipeline == pipeline {
			return &rows[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// Triggers returns the triggers of the tenant's runs of pipeline, newest first.
func Triggers(ctx context.Context, tenant *store.Tenant, pipeline string) ([]*store.Trigger, error) {
	runs, err := tenant.ListRuns(ctx, store.RunFilter{Pipelines: []string{pipeline}, Limit: store.MaxRunListLimit})
	if err != nil {
		return nil, err
	}
	var out []*store.Trigger
	for _, run := range runs {
		trigger, err := tenant.GetTrigger(ctx, run.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, trigger)
	}
	return out, nil
}
