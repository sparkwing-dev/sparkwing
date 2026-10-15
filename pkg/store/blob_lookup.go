package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (r *blobRecords) LookupRecord(ctx context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
	if !request.Budget.Deadline.IsZero() && !r.tenant.s.now().Before(request.Budget.Deadline) {
		return blob.RecordLookupResult{}, blob.ErrBudget
	}
	tx, err := r.tenant.s.beginTx(ctx)
	if err != nil {
		return blob.RecordLookupResult{}, err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, r.tenant.team, request.Authority, capability.DataRead)
	if err != nil {
		return blob.RecordLookupResult{}, err
	}
	hit, err := r.lookupRecordTx(ctx, tx, a, request)
	if err != nil {
		return hit, err
	}
	if !request.Budget.Deadline.IsZero() && !tx.now().Before(request.Budget.Deadline) {
		return blob.RecordLookupResult{}, blob.ErrBudget
	}
	return hit, tx.Commit()
}

func (r *blobRecords) lookupRecordTx(ctx context.Context, tx *storeTx, a blobActor, request blob.RecordLookup) (blob.RecordLookupResult, error) {
	result := blob.RecordLookupResult{}
	key := request.Key
	if key.Team != string(r.tenant.team) || key.Scope.Repo != a.authority.Repo {
		return result, blob.ErrDigestNotReadable
	}
	namespace, err := r.rules.RecordNamespace(key)
	if err != nil {
		return result, err
	}
	record, found, err := readBlobRecordTx(ctx, tx, key, namespace)
	if err != nil {
		return result, err
	}
	if !found || (record.ExpiresAt.Set && !record.Pinned && !record.ExpiresAt.Value.After(tx.now())) {
		return result, nil
	}
	if request.VerifiedDefaultOnly {
		facts := record.RefFacts
		if !facts.DefaultBranchVerified || !facts.Scope.Verified || facts.Scope.Ref == "" || facts.Scope.Ref != facts.DefaultBranch || facts.AppDeliveryKey == "" || facts.SHA == "" || (a.authority.Ref.DefaultBranch != "" && facts.Scope.Ref != a.authority.Ref.DefaultBranch) {
			return result, nil
		}
	}
	if err := blobRecordReadableTx(ctx, tx, a, key, namespace); err != nil {
		return result, err
	}
	if err := r.authorizeDigestTx(ctx, tx, a, key.Kind, record.Digest); err != nil {
		return result, err
	}
	if key.Kind == blob.KindOutput || (key.Kind == blob.KindMemo && key.Name == "output") {
		size, err := requireBlobTx(ctx, tx, r.tenant.team, record.Digest)
		if err != nil {
			return result, err
		}
		if size > limits.NodeOutputBytes {
			return result, blob.ErrIntegrity
		}
	}
	if record.ManifestDigest.Set {
		if err := r.authorizeDigestTx(ctx, tx, a, key.Kind, record.ManifestDigest.Value); err != nil {
			return result, err
		}
	}
	members, err := r.readMembers(ctx, tx, record)
	if err != nil {
		return result, err
	}
	// safety: manifest I/O must not carry expired read or holder rights into a result.
	a, err = resolveBlobAuthorityTx(ctx, tx, r.tenant.team, a.authority, capability.DataRead)
	if err != nil {
		return result, err
	}
	digests := []blob.Digest{record.Digest}
	if record.ManifestDigest.Set {
		digests = append(digests, record.ManifestDigest.Value)
	}
	for _, m := range members {
		digests = append(digests, m.Digest)
	}
	result.ReadSet.Reader = a.authority.Attempt.Value
	for _, digest := range digests {
		if _, err := requireBlobTx(ctx, tx, r.tenant.team, digest); err != nil {
			return result, err
		}
		own, err := readOwnBlobReceiptTx(ctx, tx, a.source, digest)
		if err != nil {
			return result, err
		}
		set, err := blobReadSetTx(ctx, tx, a, digest)
		if err != nil {
			return result, err
		}
		if err := r.rules.AuthorizeEdge(blob.EdgeRequest{TargetKind: key.Kind, Authority: a.authority, Digest: digest, ReadSet: set, OwnReceipt: own}); err != nil {
			return result, err
		}
		for i := range set.Edges {
			set.Edges[i].Record = key
		}
		if own.Set {
			set.Edges = append(set.Edges, blob.ReadEdge{Kind: blob.EdgeOwnUpload, Reader: set.Reader, Source: own.Value.Identity.Attempt.Value, Record: key, Digest: digest})
		}
		result.ReadSet.Edges = append(result.ReadSet.Edges, set.Edges...)
	}
	result.Found, result.Record, result.Members = true, record, members
	return result, nil
}

func (r *blobRecords) readMembers(ctx context.Context, tx *storeTx, record blob.Record) ([]blob.Member, error) {
	manifest := record.ManifestDigest
	if record.Key.Kind == blob.KindManifest {
		manifest = runspec.Optional[blob.Digest]{Set: true, Value: record.Digest}
	}
	if !manifest.Set {
		return nil, nil
	}
	if r.manifests == nil {
		return nil, authorityError(apierr.Unavailable, "verified manifest reader is not configured")
	}
	members, err := r.manifests.ReadBlobManifest(ctx, r.tenant.team, manifest.Value)
	if err != nil {
		return nil, err
	}
	expected := map[blob.Digest]bool{}
	for _, m := range members {
		if m.Team != string(r.tenant.team) || m.ManifestDigest != manifest.Value {
			return nil, blob.ErrIntegrity
		}
		size, err := requireBlobTx(ctx, tx, r.tenant.team, m.Digest)
		if err != nil {
			return nil, err
		}
		if size != m.Size {
			return nil, blob.ErrIntegrity
		}
		expected[m.Digest] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT member_digest FROM blob_members WHERE team=? AND manifest_digest=?", r.tenant.team, manifest.Value)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var digest blob.Digest
		if err := rows.Scan(&digest); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if !expected[digest] {
			return nil, errors.Join(blob.ErrIntegrity, rows.Close())
		}
		delete(expected, digest)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(expected) != 0 {
		return nil, blob.ErrIntegrity
	}
	return members, nil
}

func cacheMissError(err error) bool {
	return errors.Is(err, blob.ErrNotFound) || errors.Is(err, blob.ErrIntegrity) || errors.Is(err, blob.ErrBudget)
}

func (r *blobRecords) LookupMemo(ctx context.Context, request blob.MemoLookup) (blob.MemoHit, error) {
	tx, err := r.tenant.s.beginTx(ctx)
	if err != nil {
		return blob.MemoHit{}, err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, r.tenant.team, request.Authority, capability.DataRead)
	if err != nil {
		return blob.MemoHit{}, err
	}
	hit, err := r.lookupMemoTx(ctx, tx, a, request, nil)
	if cacheMissError(err) {
		return blob.MemoHit{}, nil
	}
	if err != nil {
		return hit, err
	}
	return hit, tx.Commit()
}

func (r *blobRecords) lookupMemoTx(ctx context.Context, tx *storeTx, a blobActor, request blob.MemoLookup, source *blob.Record) (blob.MemoHit, error) {
	result := blob.MemoHit{}
	key := request.Key
	if key.Repo != a.authority.Repo || key.Hash == "" || blobLocalRepo(key.Repo) {
		return result, blob.ErrDigestNotReadable
	}
	if a.capability.Data.Use != capability.DataRemoteCache && (key.Pipeline != a.pipeline || !a.authority.Attempt.Set || key.Job != a.authority.Attempt.Value.NodeID || key.Ref != a.authority.Ref.Scope) {
		return result, blob.ErrDigestNotReadable
	}
	if a.capability.Data.Use != capability.DataRemoteCache {
		if err := requireBlobMemoTx(ctx, tx, a); err != nil {
			return result, err
		}
	}
	refs := []blob.RefScope{a.authority.Ref.Scope}
	if a.capability.Data.Use == capability.DataRemoteCache {
		refs = nil
		for _, ref := range a.capability.Data.ReadRefs {
			refs = append(refs, blob.RefScope{Ref: ref, Verified: true})
		}
	} else if a.authority.Ref.DefaultBranch != "" {
		refs = append(refs, blob.RefScope{Ref: a.authority.Ref.DefaultBranch, Verified: true})
	}
	for _, ref := range refs {
		if !slices.Contains(a.capability.Data.ReadRefs, blobRefScope(ref)) {
			continue
		}
		for _, name := range []string{"output", "manifest"} {
			if !request.Budget.Deadline.IsZero() && !tx.now().Before(request.Budget.Deadline) {
				return result, blob.ErrBudget
			}
			recordKey := blob.RecordKey{Team: string(r.tenant.team), Kind: blob.KindMemo, Scope: blob.Scope{Repo: key.Repo, Ref: ref, Pipeline: key.Pipeline, Job: key.Job, Key: string(key.Hash)}, Name: name}
			hit, err := r.lookupRecordTx(ctx, tx, a, blob.RecordLookup{Key: recordKey, VerifiedDefaultOnly: a.capability.Data.Use == capability.DataRemoteCache || ref != a.authority.Ref.Scope})
			if cacheMissError(err) {
				continue
			}
			if err != nil {
				return result, err
			}
			if !hit.Found {
				continue
			}
			if source != nil {
				*source = hit.Record
			}
			result.Found = true
			result.Manifest = hit.Record.ManifestDigest
			if name == "output" {
				result.Output = runspec.Optional[blob.Digest]{Set: true, Value: hit.Record.Digest}
			} else {
				result.Manifest = runspec.Optional[blob.Digest]{Set: true, Value: hit.Record.Digest}
			}
			result.Members, result.ReadSet = hit.Members, hit.ReadSet
			if !request.Budget.Deadline.IsZero() && !tx.now().Before(request.Budget.Deadline) {
				return blob.MemoHit{}, blob.ErrBudget
			}
			return result, nil
		}
	}
	return result, nil
}

func (r *blobRecords) LookupDepcache(ctx context.Context, request blob.DepcacheLookup) (blob.DepcacheHit, error) {
	result := blob.DepcacheHit{}
	tx, err := r.tenant.s.beginTx(ctx)
	if err != nil {
		return result, err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, r.tenant.team, request.Authority, capability.DataRead)
	if err != nil {
		return result, err
	}
	if request.Key.Repo != a.authority.Repo || blobLocalRepo(request.Key.Repo) || (a.capability.Data.Use != capability.DataRemoteCache && request.Key.Ref != a.authority.Ref.Scope) {
		return result, blob.ErrDigestNotReadable
	}
	type fallback struct {
		ref    blob.RefScope
		source blob.RestoreSource
	}
	refs := []fallback{{a.authority.Ref.Scope, blob.RestoreOwnRef}}
	if a.authority.Ref.PullRequestBase.Set {
		refs = append(refs, fallback{blob.RefScope{Ref: a.authority.Ref.PullRequestBase.Value, Verified: true}, blob.RestorePRBase})
	}
	if a.authority.Ref.DefaultBranch != "" {
		refs = append(refs, fallback{blob.RefScope{Ref: a.authority.Ref.DefaultBranch, Verified: true}, blob.RestoreDefaultBranch})
	}
	if a.capability.Data.Use == capability.DataRemoteCache {
		refs = nil
		for _, ref := range a.capability.Data.ReadRefs {
			refs = append(refs, fallback{blob.RefScope{Ref: ref, Verified: true}, blob.RestoreDefaultBranch})
		}
	}
	for _, fallback := range refs {
		if !slices.Contains(a.capability.Data.ReadRefs, blobRefScope(fallback.ref)) {
			continue
		}
		if !request.Budget.Deadline.IsZero() && !tx.now().Before(request.Budget.Deadline) {
			return result, nil
		}
		key := blob.RecordKey{Team: string(r.tenant.team), Kind: blob.KindDepcache, Scope: blob.Scope{Repo: a.authority.Repo, Ref: fallback.ref, Key: request.Key.Key}, Name: request.Key.Name}
		hit, err := r.lookupRecordTx(ctx, tx, a, blob.RecordLookup{Key: key, VerifiedDefaultOnly: fallback.source == blob.RestoreDefaultBranch})
		if cacheMissError(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		if !hit.Found || (fallback.source != blob.RestoreOwnRef && !hit.Record.RefFacts.Scope.Verified) {
			continue
		}
		if !request.Budget.Deadline.IsZero() && !tx.now().Before(request.Budget.Deadline) {
			return blob.DepcacheHit{}, nil
		}
		result = blob.DepcacheHit{Found: true, Record: hit.Record, Source: fallback.source, ReadSet: hit.ReadSet}
		return result, tx.Commit()
	}
	return result, tx.Commit()
}

func (r *blobRecords) CopyMemo(ctx context.Context, request blob.CopyMemoRequest) (blob.MemoHit, error) {
	tx, err := r.tenant.s.beginTx(ctx)
	if err != nil {
		return blob.MemoHit{}, err
	}
	defer rollbackOrLog(tx)
	a, err := resolveBlobAuthorityTx(ctx, tx, r.tenant.team, request.Lookup.Authority, capability.DataWrite)
	if err != nil {
		return blob.MemoHit{}, err
	}
	if !a.authority.Attempt.Set || a.authority.Attempt.Value != request.Destination || !slices.Contains(a.capability.Grants, capability.DataRead) {
		return blob.MemoHit{}, blob.ErrDigestNotReadable
	}
	var original blob.Record
	hit, err := r.lookupMemoTx(ctx, tx, a, request.Lookup, &original)
	if cacheMissError(err) {
		return blob.MemoHit{}, nil
	}
	if err != nil || !hit.Found {
		return hit, err
	}
	digests := []blob.Digest{}
	if hit.Output.Set {
		digests = append(digests, hit.Output.Value)
	}
	if hit.Manifest.Set {
		digests = append(digests, hit.Manifest.Value)
	}
	for _, member := range hit.Members {
		digests = append(digests, member.Digest)
	}
	slices.Sort(digests)
	for _, digest := range slices.Compact(digests) {
		if err := advisoryLockTx(ctx, tx, "blob:"+string(r.tenant.team)+":"+string(digest)); err != nil {
			return blob.MemoHit{}, err
		}
		if _, err := requireBlobTx(ctx, tx, r.tenant.team, digest); err != nil {
			if cacheMissError(err) {
				return blob.MemoHit{}, nil
			}
			return blob.MemoHit{}, err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM blob_tombstones WHERE team=? AND digest=?", r.tenant.team, digest); err != nil {
			return blob.MemoHit{}, err
		}
	}
	for _, part := range []struct {
		kind   blob.Kind
		digest runspec.Optional[blob.Digest]
	}{{blob.KindOutput, hit.Output}, {blob.KindManifest, hit.Manifest}} {
		if !part.digest.Set {
			continue
		}
		key := ResultBlobKey(request.Destination, a.authority.Repo, a.authority.Ref.Scope, a.pipeline, part.kind)
		scope, err := r.rules.RecordNamespace(key)
		if err != nil {
			return blob.MemoHit{}, err
		}
		prior, found, err := readBlobRecordTx(ctx, tx, key, scope)
		if err != nil {
			return blob.MemoHit{}, err
		}
		if found && prior.Digest != part.digest.Value {
			return blob.MemoHit{}, authorityError(apierr.Conflict, "memo destination is immutable")
		}
		if found {
			continue
		}
		record := original
		record.Key, record.Digest, record.CreatedAt = key, part.digest.Value, tx.now()
		record.ExpiresAt = runspec.Optional[time.Time]{}
		record.ManifestDigest = runspec.Optional[blob.Digest]{}
		record.Pinned = false
		if err := persistBlobRecordTx(ctx, tx, record, scope, a.source); err != nil {
			return blob.MemoHit{}, err
		}
		edge := blob.ReadEdge{Kind: blob.EdgeMemoHit, Reader: request.Destination, Record: key, Digest: part.digest.Value}
		hit.ReadSet.Edges = append(hit.ReadSet.Edges, edge)
	}
	if !request.Lookup.Budget.Deadline.IsZero() && !tx.now().Before(request.Lookup.Budget.Deadline) {
		return blob.MemoHit{}, nil
	}
	return hit, tx.Commit()
}
