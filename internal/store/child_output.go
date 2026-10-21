package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// ReadChildOutput selects the child's committed output under its creating job's live authority.
func (t *Tenant) ReadChildOutput(ctx context.Context, supplied capability.Authorized) (blob.Blob, error) {
	return t.readOutput(ctx, supplied, true)
}

// ReadJobOutput selects a committed output under current membership or the declared read relation.
func (t *Tenant) ReadJobOutput(ctx context.Context, supplied capability.Authorized) (blob.Blob, error) {
	return t.readOutput(ctx, supplied, false)
}

func (t *Tenant) readOutput(ctx context.Context, supplied capability.Authorized, child bool) (blob.Blob, error) {
	supplied.Target.Pipeline = ""
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return blob.Blob{}, err
	}
	defer rollbackOrLog(tx)
	chain, err := authorityChainTx(ctx, tx, supplied.Principal.Authority, false)
	if err != nil {
		return blob.Blob{}, err
	}
	state, err := authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return blob.Blob{}, err
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return blob.Blob{}, clockErr1
	}
	snapshot := capability.AuthenticationSnapshot{Now: clockTime1, Binding: state}
	for _, node := range chain {
		snapshot.Authorities = append(snapshot.Authorities, capability.AuthorityState{Principal: node.principal})
	}
	path, run := "/api/v1/runs/{run}/jobs/{job}/output", supplied.Target.RunID
	if child {
		path, run = "/api/v1/runs/{run}/children/{child}/jobs/{job}/output", supplied.Target.ChildRunID
	}
	route, _ := capability.LookupRoute("GET", path)
	auth, err := capability.AuthenticateSnapshot(snapshot, route, capability.RequestEvidence{Target: supplied.Target})
	if err != nil {
		return blob.Blob{}, err
	}
	if auth.Principal.Capability.Team != string(t.team) || supplied.Target.Team != string(t.team) {
		return blob.Blob{}, bindingRefusal()
	}
	a, err := capability.Authorize(ctx, tx, route, auth, supplied.Target)
	if err != nil {
		return blob.Blob{}, err
	}
	row, err := readNodeTx(ctx, tx, t.team, controllerwire.NodeRef{RunID: runspec.RunID(run), NodeID: runspec.NodeID(a.Target.NodeID)})
	if err != nil {
		return blob.Blob{}, err
	}
	if row.Status != StatusDone || row.OutputRecord == nil {
		return blob.Blob{}, blob.ErrNotFound
	}
	key := *row.OutputRecord
	if key.Team != string(t.team) || key.Kind != blob.KindOutput || runspec.NodeID(key.Scope.Job) != row.Ref.NodeID {
		return blob.Blob{}, blob.ErrIntegrity
	}
	namespace, err := blob.RecordNamespace(key)
	if err != nil {
		return blob.Blob{}, err
	}
	record, found, err := readBlobRecordTx(ctx, tx, key, namespace)
	if err != nil {
		return blob.Blob{}, err
	}
	clockTime2, clockErr2 := tx.now(ctx)
	if clockErr2 != nil {
		return blob.Blob{}, clockErr2
	}
	if !found || !record.Pinned && record.ExpiresAt.Set && !record.ExpiresAt.Value.After(clockTime2) {
		return blob.Blob{}, blob.ErrNotFound
	}
	size, err := requireBlobTx(ctx, tx, t.team, record.Digest)
	if err != nil {
		return blob.Blob{}, err
	}
	if err := tx.Commit(); err != nil {
		return blob.Blob{}, err
	}
	return blob.Blob{Team: string(t.team), Digest: record.Digest, Size: size}, nil
}
