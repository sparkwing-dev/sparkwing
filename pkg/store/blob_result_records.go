package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// SetResultBlobRecords configures transactional result verification before the store is shared.
func (s *Store) SetResultBlobRecords(manifests BlobManifestReader) {
	s.resultRecords = &blobRecords{manifests: manifests, rules: sharedBlobRules{}}
}

func (r *blobRecords) AttachResultRecords(ctx context.Context, tx *storeTx, team Team, actor Actor, row NodeRow, result NodeResult) error {
	attempt := runspec.AttemptIdentity{Team: string(team), RunID: row.Ref.RunID, NodeID: row.Ref.NodeID, Attempt: row.Attempt, Generation: row.Generation}
	if attempt.Attempt <= 0 || attempt.Generation <= 0 || actor.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: actor.CapabilityPrefix}) {
		return blob.ErrDigestNotReadable
	}
	input := blob.Authority{CapabilityRef: actor.CapabilityPrefix, Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}}
	a, err := resolveBlobAuthorityTx(ctx, tx, team, input, capability.DataWrite)
	if err != nil {
		return err
	}
	records := *r
	records.tenant = &Tenant{s: tx.store, team: team}
	var output, manifest blob.RecordLookupResult
	for _, part := range []struct {
		key  *blob.RecordKey
		kind blob.Kind
		hit  *blob.RecordLookupResult
	}{{result.OutputRecord, blob.KindOutput, &output}, {result.ManifestRecord, blob.KindManifest, &manifest}} {
		if part.key == nil {
			continue
		}
		if *part.key != blob.ResultRecordKey(attempt, a.authority.Repo, a.authority.Ref.Scope, a.pipeline, part.kind) {
			return blob.ErrDigestNotReadable
		}
		*part.hit, err = records.lookupRecordTx(ctx, tx, a, blob.RecordLookup{Key: *part.key}, capability.DataWrite)
		if err != nil {
			return err
		}
		if !part.hit.Found {
			return blob.ErrNotFound
		}
	}
	if output.Found && manifest.Found && output.Record.ManifestDigest.Set && output.Record.ManifestDigest.Value != manifest.Record.Digest {
		return blob.ErrIntegrity
	}
	return nil
}

var _ nodeResultRecords = (*blobRecords)(nil)
