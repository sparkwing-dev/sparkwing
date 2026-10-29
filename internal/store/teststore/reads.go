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

// Triggers returns the triggers of the tenant's runs, newest first, limited to
// pipelines when any are named.
func Triggers(ctx context.Context, tenant *store.Tenant, pipelines ...string) ([]*store.Trigger, error) {
	runs, err := tenant.ListRuns(ctx, store.RunFilter{Pipelines: pipelines, Limit: store.MaxRunListLimit})
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

// AccountByEmail returns the account registered under email.
func AccountByEmail(t T, st *store.Store, email string) store.Account {
	t.Helper()
	db := SQL(t, st)
	var id string
	if err := db.QueryRowContext(t.Context(), rebind(st, `SELECT id FROM accounts WHERE email=?`), email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	account, err := st.AsOperator().Account(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return account
}
