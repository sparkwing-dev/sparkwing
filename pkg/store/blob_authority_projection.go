package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ResolveDataAuthority returns the live sealed scope and exact uploader identity.
func (t *Tenant) ResolveDataAuthority(ctx context.Context, authorized capability.Authorized) (blob.Authority, error) {
	var authority blob.Authority
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		ref := authorized.Principal.Authority
		if ref.Kind != capability.AuthorityCapability || ref.ID == "" || authorized.Target.Team != string(t.team) {
			return bindingRefusal()
		}
		grant := capability.DataRead
		if slices.Contains(authorized.Access.Grants, capability.DataWrite) {
			grant = capability.DataWrite
		} else if authorized.Access.Binding == capability.BindSourceBundle && slices.Contains(authorized.Access.Grants, capability.SourceRead) {
			grant = capability.SourceRead
		}
		a, err := resolveBlobAuthorityTx(ctx, tx, t.team, blob.Authority{CapabilityRef: ref.ID}, grant)
		authority = a.authority
		return err
	})
	if err != nil {
		return blob.Authority{}, err
	}
	return authority, nil
}

// ReadDataBlob resolves authorized existence and size in one store snapshot.
func (t *Tenant) ReadDataBlob(ctx context.Context, authority blob.Authority, digest blob.Digest) (blob.Blob, error) {
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return blob.Blob{}, err
	}
	defer rollbackOrLog(tx)
	metadata, err := readDataBlobTx(ctx, tx, t.team, authority, digest)
	if err != nil {
		return blob.Blob{}, err
	}
	return metadata, tx.Commit()
}

func readDataBlobTx(ctx context.Context, tx *storeTx, team Team, authority blob.Authority, digest blob.Digest) (blob.Blob, error) {
	presence, err := readableBlobsTx(ctx, tx, team, blob.HasManyRequest{Authority: authority, Digests: []blob.Digest{digest}})
	if err != nil {
		return blob.Blob{}, err
	}
	if len(presence.Present) != 1 {
		return blob.Blob{}, blob.ErrDigestNotReadable
	}
	size, err := requireBlobTx(ctx, tx, team, digest)
	return blob.Blob{Team: string(team), Digest: digest, Size: size}, err
}

// ChargeDataDownload rechecks the read edge and size before committing signed-byte usage.
func (t *Tenant) ChargeDataDownload(ctx context.Context, authority blob.Authority, metadata blob.Blob, charge DownloadCharge) (DownloadCharged, error) {
	var charged DownloadCharged
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		live, err := readDataBlobTx(ctx, tx, t.team, authority, metadata.Digest)
		if err != nil {
			return err
		}
		if live != metadata || charge.Bytes != live.Size {
			return blob.ErrIntegrity
		}
		charged, err = t.chargeDownloadTx(ctx, tx, charge)
		return err
	})
	if err != nil {
		return DownloadCharged{}, err
	}
	return charged, nil
}
