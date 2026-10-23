package store

import (
	"context"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

type blobRecordMutation struct {
	record  blob.Record
	scope   string
	members []blob.Member
}

func (r *blobRecords) mutateRecordsTx(ctx context.Context, tx *storeTx, a blobActor, batch []blobRecordMutation, copied bool, target *runstate.NodeRow) ([]blob.Record, error) {
	var digests []blob.Digest
	seen := map[blob.RecordKey]blob.Record{}
	for _, change := range batch {
		key := change.record.Key
		if prior, ok := seen[key]; ok && (prior.Digest != change.record.Digest || prior.ManifestDigest != change.record.ManifestDigest) {
			return nil, blob.ErrIntegrity
		}
		seen[key] = change.record
		digests = append(digests, change.record.Digest)
		if change.record.ManifestDigest.Set {
			digests = append(digests, change.record.ManifestDigest.Value)
		}
		for _, member := range change.members {
			digests = append(digests, member.Digest)
		}
	}
	slices.Sort(digests)
	digests = slices.Compact(digests)
	for _, digest := range digests {
		if err := advisoryLockTx(ctx, tx, "blob:"+string(r.tenant.team)+":"+string(digest)); err != nil {
			return nil, err
		}
		if _, err := requireBlobTx(ctx, tx, r.tenant.team, digest); err != nil {
			return nil, err
		}
	}
	result := make([]blob.Record, len(batch))
	replayed := make([]bool, len(batch))
	for i, change := range batch {
		record := change.record
		if err := checkRecordMutationKey(a, record.Key, target); err != nil {
			return nil, err
		}
		if !copied {
			for _, digest := range digests {
				if err := r.authorizeDigestTx(ctx, tx, a, record.Key.Kind, digest); err != nil {
					return nil, err
				}
			}
		}
		prior, found, err := readBlobRecordTx(ctx, tx, record.Key, change.scope)
		if err != nil {
			return nil, err
		}
		if found && (record.Key.Kind == blob.KindOutput || record.Key.Kind == blob.KindManifest) {
			if prior.Digest != record.Digest || (!copied && (prior.ManifestDigest != record.ManifestDigest || prior.ExpiresAt != record.ExpiresAt || !reflect.DeepEqual(prior.Source, record.Source))) {
				return nil, authorityError(apierr.Conflict, "job output record is immutable")
			}
			replayed[i], result[i] = true, prior
		} else {
			result[i] = record
		}
	}
	current, err := resolveBlobAuthorityTx(ctx, tx, r.tenant.team, a.authority, capability.DataWrite)
	if err != nil {
		return nil, err
	}
	if copied && !slices.Contains(current.capability.Grants, capability.DataRead) {
		return nil, blob.ErrDigestNotReadable
	}
	for i, change := range batch {
		if replayed[i] {
			continue
		}
		result[i].CreatedAt = tx.now(ctx)
		if err := persistBlobRecordTx(ctx, tx, result[i], change.scope, current.source, target); err != nil {
			return nil, err
		}
		if err := persistBlobMembersTx(ctx, tx, r.tenant.team, result[i], change.members); err != nil {
			return nil, err
		}
	}
	for _, digest := range digests {
		if err := clearBlobTombstoneTx(ctx, tx, r.tenant.team, digest); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func checkRecordMutationKey(a blobActor, key blob.RecordKey, target *runstate.NodeRow) error {
	if target == nil {
		return checkBlobWriteKey(a, key)
	}
	if a.source.Kind != blob.UploadOwnerUnit || runspec.RunID(a.source.RunID) != target.Ref.RunID || (key.Kind != blob.KindOutput && key.Kind != blob.KindManifest) || key != materializedKey(Team(a.capability.Team), a, *target, key.Kind) || a.capability.Data.WriteRef != key.Scope.Ref.CapabilityScope() {
		return blob.ErrDigestNotReadable
	}
	return nil
}
