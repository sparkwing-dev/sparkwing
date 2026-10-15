package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// ReadBlob returns live metadata after rechecking tenant authority and read edges.
func (t *Tenant) ReadBlob(ctx context.Context, authority blob.Authority, digest blob.Digest) (blob.Blob, error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return blob.Blob{}, err
	}
	defer rollbackOrLog(tx)
	presence, err := readableBlobsTx(ctx, tx, t.team, blob.HasManyRequest{Authority: authority, Digests: []blob.Digest{digest}})
	if err != nil {
		return blob.Blob{}, err
	}
	if len(presence.Present) != 1 {
		return blob.Blob{}, blob.ErrNotFound
	}
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT b.size FROM blobs b WHERE b.team=? AND b.digest=?
AND NOT EXISTS (SELECT 1 FROM blob_tombstones t WHERE t.team=b.team AND t.digest=b.digest)`, t.team, digest).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return blob.Blob{}, blob.ErrNotFound
	}
	if err != nil {
		return blob.Blob{}, err
	}
	if size < 0 {
		return blob.Blob{}, blob.ErrIntegrity
	}
	if err := tx.Commit(); err != nil {
		return blob.Blob{}, err
	}
	return blob.Blob{Team: string(t.team), Digest: digest, Size: size}, nil
}
