package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// CheckDataAccess rechecks persisted authority and data edges in one transaction.
func (t *Tenant) CheckDataAccess(ctx context.Context, authorized capability.Authorized, target capability.DataTarget) error {
	if authorized.Principal.Capability.Team != string(t.team) {
		return authorityError(apierr.Forbidden, "data route requires tenant capability authority")
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := tx.CheckDataAccess(ctx, authorized, target); err != nil {
		return err
	}
	return tx.Commit()
}

func (tx *storeTx) CheckDataAccess(ctx context.Context, authorized capability.Authorized, target capability.DataTarget) error {
	c := authorized.Principal.Capability
	ref := authorized.Principal.Authority
	if ref.Kind != capability.AuthorityCapability || ref.ID == "" || c.Prefix != ref.ID || c.Team == "" {
		return authorityError(apierr.Forbidden, "data route requires tenant capability authority")
	}
	p, err := tx.ResolveAuthority(ctx, ref)
	if err != nil {
		return err
	}
	if p.Capability.Team != c.Team {
		return bindingRefusal()
	}
	c = p.Capability
	team := Team(c.Team)
	input := blob.Authority{CapabilityRef: c.Prefix}
	if target.RunID != "" && target.NodeID != "" && target.Attempt > 0 && target.Generation > 0 {
		input.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: c.Team, RunID: target.RunID, NodeID: target.NodeID, Attempt: target.Attempt, Generation: target.Generation}}
	}
	switch target.Operation {
	case capability.DataUpload, capability.DataCommit, capability.DataRecord:
		return checkBlobDataWriteTx(ctx, tx, team, input, target)
	case capability.DataLookup:
		if target.Digest == "" {
			return checkBlobSelectionTx(ctx, tx, team, input, target)
		}
		fallthrough
	case capability.DataDownload, capability.DataHasMany:
		presence, err := readableBlobsTx(ctx, tx, team, blob.HasManyRequest{Authority: input, Digests: []blob.Digest{blob.Digest(target.Digest)}})
		if err != nil {
			return err
		}
		if len(presence.Present) != 1 {
			return blob.ErrDigestNotReadable
		}
		return nil
	default:
		return authorityError(apierr.BadRequest, "unknown data operation")
	}
}

func checkBlobSelectionTx(ctx context.Context, tx *storeTx, team Team, input blob.Authority, target capability.DataTarget) error {
	a, err := resolveBlobAuthorityTx(ctx, tx, team, input, capability.DataRead)
	if err != nil {
		return err
	}
	kind := blob.Kind(target.RecordKind)
	if target.RecordName == "" || !slices.Contains(blob.Kinds, kind) {
		return authorityError(apierr.BadRequest, "logical lookup requires a record kind and name")
	}
	if a.capability.Data.Use == capability.DataRemoteCache && (kind == blob.KindBinary || kind == blob.KindSourceBundle) {
		return blob.ErrDigestNotReadable
	}
	return nil
}

func checkBlobDataWriteTx(ctx context.Context, tx *storeTx, team Team, input blob.Authority, target capability.DataTarget) error {
	resolved, err := resolveBlobAuthorityTx(ctx, tx, team, input, capability.DataWrite)
	if err != nil {
		return err
	}
	source := resolved.source
	if (target.RunID != "" && target.RunID != source.RunID) || (target.NodeID != "" && (!source.Attempt.Set || target.NodeID != source.Attempt.Value.NodeID)) || (target.Attempt != 0 && (!source.Attempt.Set || target.Attempt != source.Attempt.Value.Attempt)) || (target.Generation != 0 && (!source.Attempt.Set || target.Generation != source.Attempt.Value.Generation)) {
		return blob.ErrDigestNotReadable
	}
	if target.RecordKind != "" && !blobKindAllowed(source, blob.Kind(target.RecordKind)) {
		return blob.ErrDigestNotReadable
	}
	if target.Operation == capability.DataRecord && target.Digest == "" && target.RecordKind == string(blob.KindMemo) && target.RecordName != "" {
		if !resolved.authority.Attempt.Set || !slices.Contains(resolved.capability.Grants, capability.DataRead) {
			return blob.ErrDigestNotReadable
		}
		return requireBlobMemoTx(ctx, tx, resolved)
	}
	if !validBlobDigest(blob.Digest(target.Digest)) {
		return authorityError(apierr.BadRequest, "invalid blob digest")
	}
	if target.Operation != capability.DataRecord {
		return nil
	}
	if _, err := requireBlobTx(ctx, tx, team, blob.Digest(target.Digest)); err != nil {
		return err
	}
	receipt, err := readOwnBlobReceiptTx(ctx, tx, source, blob.Digest(target.Digest))
	if err != nil {
		return err
	}
	set, err := blobReadSetTx(ctx, tx, resolved, blob.Digest(target.Digest))
	if err != nil {
		return err
	}
	if !receipt.Set && len(set.Edges) == 0 {
		return blob.ErrDigestNotReadable
	}
	return nil
}

var (
	_ capability.DataAccessChecker = (*Tenant)(nil)
	_ capability.DataAccessChecker = (*storeTx)(nil)
)
