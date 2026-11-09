package store

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// ErrNoOutput reports a job that has no readable committed output: unfinished,
// skipped, run without a dry-run body, or its record expired. It wraps
// blob.ErrNotFound; bytes missing behind a live record stay a plain blob.ErrNotFound.
var ErrNoOutput = fmt.Errorf("job has no committed output: %w", blob.ErrNotFound)

// ReadChildOutput selects the child's committed output under its creating job's live authority.
func (t *Tenant) ReadChildOutput(ctx context.Context, supplied capability.Authorized) (blob.Blob, error) {
	return t.readOutput(ctx, supplied, true, blob.KindOutput)
}

// ReadJobOutput selects a committed output under current membership or the declared read relation.
func (t *Tenant) ReadJobOutput(ctx context.Context, supplied capability.Authorized) (blob.Blob, error) {
	return t.readOutput(ctx, supplied, false, blob.KindOutput)
}

func (t *Tenant) readOutput(ctx context.Context, supplied capability.Authorized, child bool, kind blob.Kind) (blob.Blob, error) {
	supplied.Target.Pipeline = ""
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return blob.Blob{}, err
	}
	defer rollbackOrLog(tx)
	chain, err := teamAuthorityChainTx(ctx, tx, t.team, supplied.Principal.Authority, false, sameTeam)
	if err != nil {
		return blob.Blob{}, err
	}
	state, err := authenticationBindingTx(ctx, tx, chain[0].principal.Capability)
	if err != nil {
		return blob.Blob{}, err
	}
	snapshot := capability.AuthenticationSnapshot{Now: tx.now(ctx), Binding: state}
	for _, node := range chain {
		snapshot.Authorities = append(snapshot.Authorities, capability.AuthorityState{Principal: node.principal})
	}
	path, run := "/api/v1/runs/{run}/jobs/{job}/output", supplied.Target.RunID
	if child {
		path, run = "/api/v1/runs/{run}/children/{child}/jobs/{job}/output", supplied.Target.ChildRunID
	}
	if kind == blob.KindManifest {
		path = "/api/v1/runs/{run}/jobs/{job}/artifacts"
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
	row, err := readNodeTx(ctx, tx, t.team, runspec.NodeRef{RunID: runspec.RunID(run), NodeID: runspec.NodeID(a.Target.NodeID)})
	if err != nil {
		return blob.Blob{}, err
	}
	keyPtr := row.OutputRecord
	if kind == blob.KindManifest {
		keyPtr = row.ManifestRecord
	}
	if row.Status != runstate.StatusDone || keyPtr == nil {
		return blob.Blob{}, ErrNoOutput
	}
	key := *keyPtr
	if key.Team != string(t.team) || key.Kind != kind || runspec.NodeID(key.Scope.Job) != row.Ref.NodeID {
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
	if !found || !record.Pinned && record.ExpiresAt.Set && !record.ExpiresAt.Value.After(tx.now(ctx)) {
		return blob.Blob{}, ErrNoOutput
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

// ReadJobManifest selects a committed artifact manifest under live team membership.
func (t *Tenant) ReadJobManifest(ctx context.Context, supplied capability.Authorized) (blob.Blob, error) {
	return t.readOutput(ctx, supplied, false, blob.KindManifest)
}

// ChargeArtifactDownload rechecks membership before accounting for a signed artifact download.
func (t *Tenant) ChargeArtifactDownload(ctx context.Context, a capability.Authorized, charge DownloadCharge) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		route, _ := capability.LookupRoute("GET", "/api/v1/runs/{run}/jobs/{job}/artifacts")
		if _, _, err := t.reauthorizeTx(ctx, tx, a, route, true); err != nil {
			return err
		}
		_, err := t.chargeDownloadTx(ctx, tx, charge)
		return err
	})
}
