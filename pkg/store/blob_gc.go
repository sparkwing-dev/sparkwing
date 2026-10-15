package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// BlobDeleter removes an immutable object; absence is an idempotent success.
type BlobDeleter interface {
	DeleteBlob(context.Context, blob.Blob) error
}

const reachableBlobsSQL = `WITH RECURSIVE reachable(team,digest) AS (
SELECT team,digest FROM blob_records
UNION SELECT team,manifest_digest FROM blob_records WHERE manifest_digest IS NOT NULL
UNION SELECT team,digest FROM blob_receipts
UNION SELECT m.team,m.member_digest FROM blob_members m JOIN reachable r ON r.team=m.team AND r.digest=m.manifest_digest
) `

// MarkBlobTombstones starts the grace period for blobs outside all live reference graphs.
// Receipt authority remains a root until run retention removes its uploader.
func (o *Operator) MarkBlobTombstones(ctx context.Context, lease SingletonLease, limit int) (int64, error) {
	if limit <= 0 {
		return 0, ErrInvalidInput
	}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackOrLog(tx)
	if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
		return 0, err
	}
	if err := releaseExpiredBlobUploadQuotasTx(ctx, tx, limit); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, reachableBlobsSQL+`SELECT b.team,b.digest FROM blobs b WHERE NOT EXISTS(SELECT 1 FROM reachable r WHERE r.team=b.team AND r.digest=b.digest) AND NOT EXISTS(SELECT 1 FROM blob_tombstones t WHERE t.team=b.team AND t.digest=b.digest) ORDER BY b.team,b.digest LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		team   Team
		digest blob.Digest
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.team, &c.digest); err != nil {
			return 0, errors.Join(err, rows.Close())
		}
		candidates = append(candidates, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	var marked int64
	for _, c := range candidates {
		if err := advisoryLockTx(ctx, tx, "blob:"+string(c.team)+":"+string(c.digest)); err != nil {
			return 0, err
		}
		reachable, err := blobReachableTx(ctx, tx, c.team, c.digest)
		if err != nil {
			return 0, err
		}
		if reachable {
			continue
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES (?,?,?) ON CONFLICT(team,digest) DO NOTHING", c.team, c.digest, tx.now().UnixNano())
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		marked += count
	}
	return marked, tx.Commit()
}

func blobReachableTx(ctx context.Context, tx *storeTx, team Team, digest blob.Digest) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, reachableBlobsSQL+"SELECT COUNT(*) FROM reachable WHERE team=? AND digest=?", team, digest).Scan(&exists)
	return exists > 0, err
}

// CollectBlobTombstones rechecks reachability under the blob lock before removing bytes.
// A deletion error preserves existence and its tombstone for the next leased pass.
func (o *Operator) CollectBlobTombstones(ctx context.Context, lease SingletonLease, limit int, deleter BlobDeleter) (int64, error) {
	if limit <= 0 || deleter == nil {
		return 0, ErrInvalidInput
	}
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackOrLog(tx)
	if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT team,digest FROM blob_tombstones WHERE marked_at<=? ORDER BY team,digest LIMIT ?", tx.now().Add(-limits.BlobGCGrace).UnixNano(), limit)
	if err != nil {
		return 0, err
	}
	var candidates []blob.Blob
	for rows.Next() {
		var b blob.Blob
		if err := rows.Scan(&b.Team, &b.Digest); err != nil {
			return 0, errors.Join(err, rows.Close())
		}
		candidates = append(candidates, b)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	var removed int64
	for _, b := range candidates {
		deleted, err := o.collectBlob(ctx, lease, b, deleter)
		if err != nil {
			return removed, err
		}
		if deleted {
			removed++
		}
	}
	return removed, nil
}

func (o *Operator) collectBlob(ctx context.Context, lease SingletonLease, b blob.Blob, deleter BlobDeleter) (bool, error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
		return false, err
	}
	if err := advisoryLockTx(ctx, tx, "blob:"+b.Team+":"+string(b.Digest)); err != nil {
		return false, err
	}
	var marked, created int64
	err = tx.QueryRowContext(ctx, "SELECT t.marked_at,b.size,b.created_at FROM blob_tombstones t JOIN blobs b ON b.team=t.team AND b.digest=t.digest WHERE t.team=? AND t.digest=?", b.Team, b.Digest).Scan(&marked, &b.Size, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if tx.now().Before(time.Unix(0, marked).Add(limits.BlobGCGrace)) {
		return false, nil
	}
	reachable, err := blobReachableTx(ctx, tx, Team(b.Team), b.Digest)
	if err != nil {
		return false, err
	}
	if reachable {
		_, err = tx.ExecContext(ctx, "DELETE FROM blob_tombstones WHERE team=? AND digest=?", b.Team, b.Digest)
		if err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	b.CreatedAt = time.Unix(0, created).UTC()
	if err := deleter.DeleteBlob(ctx, b); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM blob_members WHERE team=? AND (manifest_digest=? OR member_digest=?)", b.Team, b.Digest, b.Digest); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM blobs WHERE team=? AND digest=?", b.Team, b.Digest); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
