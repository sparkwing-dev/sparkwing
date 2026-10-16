package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ReadChildOutput selects the child's committed output under its creating job's live authority.
func (t *Tenant) ReadChildOutput(ctx context.Context, supplied capability.Authorized) (blob.Blob, error) {
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
	snapshot := capability.AuthenticationSnapshot{Now: tx.now(), Binding: state}
	for _, node := range chain {
		snapshot.Authorities = append(snapshot.Authorities, capability.AuthorityState{Principal: node.principal, MembershipValid: true})
	}
	route, _ := capability.LookupRoute("GET", "/api/v1/runs/{run}/children/{child}/jobs/{job}/output")
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
	row, err := readNodeTx(ctx, tx, t.team, NodeRef{RunID: a.Target.ChildRunID, NodeID: a.Target.NodeID})
	if err != nil {
		return blob.Blob{}, err
	}
	if row.Status != StatusDone || row.OutputRecord == nil {
		return blob.Blob{}, blob.ErrNotFound
	}
	key := *row.OutputRecord
	if key.Team != string(t.team) || key.Kind != blob.KindOutput || key.Scope.Job != row.Ref.NodeID {
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
	if !found || !record.Pinned && record.ExpiresAt.Set && !record.ExpiresAt.Value.After(tx.now()) {
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
