package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (r *blobRecords) CopyMaterializedRecords(ctx context.Context, tx *storeTx, team Team, actor Actor, ref controllerwire.NodeRef, source MaterializeSource) (materializedRecords, error) {
	result := materializedRecords{}
	a, err := resolveBlobAuthorityTx(ctx, tx, team, blob.Authority{CapabilityRef: actor.CapabilityPrefix}, capability.DataWrite)
	if err != nil {
		return result, err
	}
	if a.source.Kind != blob.UploadOwnerUnit || a.source.RunID != ref.RunID || !slices.Contains(a.capability.Grants, capability.DataRead) {
		return result, blob.ErrDigestNotReadable
	}
	target, err := readNodeTx(ctx, tx, team, ref)
	if err != nil {
		return result, err
	}
	records := *r
	records.tenant = &Tenant{s: tx.store, team: team}
	keys, err := materializationKeysTx(ctx, tx, team, a, target, source)
	if err != nil {
		return result, err
	}
	var batch []blobRecordMutation
	var expiries []time.Time
	for _, key := range keys {
		scope, err := blob.RecordNamespace(key)
		if err != nil {
			return result, err
		}
		record, found, err := readBlobRecordTx(ctx, tx, key, scope)
		if err != nil {
			return result, err
		}
		if !found || record.ExpiresAt.Set && !record.Pinned && !record.ExpiresAt.Value.After(tx.now()) {
			return result, blob.ErrNotFound
		}
		if record.ExpiresAt.Set && !record.Pinned {
			expiries = append(expiries, record.ExpiresAt.Value)
		}
		if record.Key.Scope.Repo != a.authority.Repo || record.Key.Scope.Pipeline != a.pipeline || record.Key.Scope.Job != ref.NodeID {
			return result, blob.ErrDigestNotReadable
		}
		if record.Key.Scope.Ref != a.authority.Ref.Scope {
			facts := record.RefFacts
			if source.Kind != SourceMemo || !facts.DefaultBranchVerified || !facts.Scope.Verified || facts.AppDeliveryKey == "" || facts.SHA == "" || facts.Scope.Ref != a.authority.Ref.DefaultBranch || !slices.Contains(a.capability.Data.ReadRefs, facts.Scope.CapabilityScope()) {
				return result, blob.ErrDigestNotReadable
			}
		}
		members, err := records.readMembers(ctx, tx, record)
		if err != nil {
			return result, err
		}
		parts := []struct {
			kind   blob.Kind
			digest blob.Digest
		}{{key.Kind, record.Digest}}
		if key.Kind == blob.KindMemo {
			parts[0].kind = blob.KindOutput
			if key.Name == "manifest" {
				parts = nil
			}
		}
		if record.ManifestDigest.Set {
			parts = append(parts, struct {
				kind   blob.Kind
				digest blob.Digest
			}{blob.KindManifest, record.ManifestDigest.Value})
		}
		for _, part := range parts {
			if part.kind != blob.KindOutput && part.kind != blob.KindManifest {
				return result, blob.ErrIntegrity
			}
			if part.kind == blob.KindOutput {
				size, err := requireBlobTx(ctx, tx, team, part.digest)
				if err != nil {
					return result, err
				}
				if size > limits.NodeOutputBytes {
					return result, blob.ErrIntegrity
				}
			}
			copy := record
			copy.Key = materializedKey(team, a, target, part.kind)
			copy.Digest, copy.ManifestDigest = part.digest, runspec.Optional[blob.Digest]{}
			copy.ExpiresAt, copy.Pinned = runspec.Optional[time.Time]{}, false
			namespace, err := blob.RecordNamespace(copy.Key)
			if err != nil {
				return result, err
			}
			mutation := blobRecordMutation{record: copy, scope: namespace}
			if part.kind == blob.KindManifest {
				mutation.members = members
				result.Manifest = &copy.Key
			} else {
				result.Output = &copy.Key
			}
			batch = append(batch, mutation)
		}
	}
	if err := records.verifyMaterializedBlobs(ctx, tx, batch); err != nil {
		return materializedRecords{}, err
	}
	if err := checkOwnerTx(ctx, tx, team, actor, ref.RunID, tx.now()); err != nil {
		return materializedRecords{}, err
	}
	for _, expiry := range expiries {
		if !expiry.After(tx.now()) {
			return materializedRecords{}, blob.ErrNotFound
		}
	}
	if _, err := records.mutateRecordsTx(ctx, tx, a, batch, true, &target); err != nil {
		return materializedRecords{}, err
	}
	return result, nil
}

func (r *blobRecords) verifyMaterializedBlobs(ctx context.Context, tx *storeTx, batch []blobRecordMutation) (pending error) {
	if len(batch) == 0 {
		return nil
	}
	verifier, ok := r.manifests.(interface {
		VerifyBlob(context.Context, blob.Blob) error
	})
	if !ok {
		return authorityError(apierr.Unavailable, "verified result byte reader is not configured")
	}
	digests := map[blob.Digest]bool{}
	for _, change := range batch {
		digests[change.record.Digest] = true
		for _, member := range change.members {
			digests[member.Digest] = true
		}
	}
	for digest := range digests {
		size, err := requireBlobTx(ctx, tx, r.tenant.team, digest)
		if err != nil {
			return err
		}
		expected := blob.Blob{Team: string(r.tenant.team), Digest: digest, Size: size}
		_, err = tx.blobIO.read(fmt.Sprintf("blob:%s:%d", digest, size), func(ctx context.Context) ([]blob.Member, error) {
			return nil, verifier.VerifyBlob(ctx, expected)
		})
		if errors.Is(err, errBlobIOPending) {
			pending = err
		} else if err != nil {
			return err
		}
	}
	return pending
}

func materializedKey(team Team, a blobActor, row NodeRow, kind blob.Kind) blob.RecordKey {
	return blob.MaterializedRecordKey(string(team), row.Ref.RunID, row.Ref.NodeID, row.StateSeq+1, a.authority.Repo, a.authority.Ref.Scope, a.pipeline, kind)
}

func materializationKeysTx(ctx context.Context, tx *storeTx, team Team, a blobActor, target NodeRow, source MaterializeSource) ([]blob.RecordKey, error) {
	if source.Kind == SourceCoalesced {
		return nil, authorityError(apierr.Unavailable, "coalesced source read-edge authority is not configured")
	}
	if source.Kind == SourceMemo {
		key := source.Record
		var spec nodeproto.JobSpec
		if json.Unmarshal(target.Spec, &spec) != nil || spec.Memo == nil || key.Team != string(team) || key.Kind != blob.KindMemo || key.Scope.Repo != a.authority.Repo || key.Scope.Pipeline != a.pipeline || key.Scope.Job != target.Ref.NodeID || !slices.Contains(a.capability.Data.ReadRefs, key.Scope.Ref.CapabilityScope()) || key.Name != "output" && key.Name != "manifest" {
			return nil, blob.ErrDigestNotReadable
		}
		return []blob.RecordKey{key}, nil
	}
	ref, err := materializationRef(team, source)
	if err != nil {
		return nil, err
	}
	var repo, pipeline string
	var request []byte
	if err := tx.QueryRowContext(ctx, "SELECT repo_key,pipeline,request_json FROM runs WHERE team=? AND id=?", team, ref.RunID).Scan(&repo, &pipeline, &request); err != nil {
		return nil, err
	}
	var sealed runspec.SealedRequest
	if err := json.Unmarshal(request, &sealed); err != nil {
		return nil, err
	}
	if repo != string(a.authority.Repo) || pipeline != a.pipeline || ref.NodeID != target.Ref.NodeID || blob.ScopeFor(sealed.Provenance) != a.authority.Ref.Scope {
		return nil, blob.ErrDigestNotReadable
	}
	row, err := readNodeTx(ctx, tx, team, ref)
	if err != nil {
		return nil, err
	}
	var keys []blob.RecordKey
	for _, key := range []*blob.RecordKey{row.OutputRecord, row.ManifestRecord} {
		if key != nil {
			if key.Team != string(team) || key.Scope.Ref != a.authority.Ref.Scope {
				return nil, blob.ErrDigestNotReadable
			}
			keys = append(keys, *key)
		}
	}
	return keys, nil
}

var _ materializationRecords = (*blobRecords)(nil)

func tryMaterializationTx(ctx context.Context, tx *storeTx, team Team, request ReconcileRequest) (bool, error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT materialize_result"); err != nil {
		return false, err
	}
	_, err := transitionNodeTx(ctx, tx, team, NodeTransition{Ref: request.Ref, Actor: request.Actor, Cause: CauseMaterialize, Extra: TransitionExtra{Materialize: request.Source}}, tx.now())
	if err != nil {
		if !cacheMissError(err) {
			return false, err
		}
		if _, rollback := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT materialize_result"); rollback != nil {
			return false, errors.Join(err, rollback)
		}
	}
	_, released := tx.ExecContext(ctx, "RELEASE SAVEPOINT materialize_result")
	return err == nil, released
}
