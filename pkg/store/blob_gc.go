package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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
UNION SELECT team,digest FROM source_uploads
UNION SELECT team,digest FROM blob_receipts
UNION SELECT team,digest FROM uploads WHERE store='blob' AND committed_at=0 AND abandoned_at IS NULL
UNION SELECT m.team,m.member_digest FROM blob_members m JOIN reachable r ON r.team=m.team AND r.digest=m.manifest_digest
) `

// MarkBlobTombstones starts the grace period for blobs outside all live reference graphs.
// Receipt authority remains a root until run retention removes its uploader.
func (o *Operator) MarkBlobTombstones(ctx context.Context, lease SingletonLease, limit int) (int64, error) {
	if limit <= 0 {
		return 0, ErrInvalidInput
	}
	if err := o.abandonExpiredBlobUploads(ctx, lease, limit); err != nil {
		return 0, err
	}
	var marked int64
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		marked = 0
		if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
			return err
		}
		candidates, err := teamKeysTx(ctx, tx, reachableBlobsSQL+`SELECT b.team,b.digest FROM blobs b WHERE NOT EXISTS(SELECT 1 FROM reachable r WHERE r.team=b.team AND r.digest=b.digest) AND NOT EXISTS(SELECT 1 FROM blob_tombstones t WHERE t.team=b.team AND t.digest=b.digest) ORDER BY b.team,b.digest LIMIT ?`, limit)
		if err != nil {
			return err
		}
		for _, c := range candidates {
			if err := advisoryLockTx(ctx, tx, "blob:"+c.team+":"+c.key); err != nil {
				return err
			}
			reachable, err := blobReachableTx(ctx, tx, Team(c.team), blob.Digest(c.key))
			if err != nil {
				return err
			}
			if reachable {
				continue
			}
			result, err := tx.ExecContext(ctx, "INSERT INTO blob_tombstones(team,digest,marked_at) VALUES (?,?,?) ON CONFLICT(team,digest) DO NOTHING", c.team, c.key, tx.now().UnixNano())
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			marked += count
		}
		return nil
	})
	return marked, err
}

// safety: one ticket per transaction keeps cleanup from holding one ticket's
// digest and quota locks while it waits on another (STORE-DATA.md lock order).
func (o *Operator) abandonExpiredBlobUploads(ctx context.Context, lease SingletonLease, limit int) error {
	var expired []teamKey
	err := o.s.inTx(ctx, func(tx *storeTx) (err error) {
		expired, err = teamKeysTx(ctx, tx, "SELECT team,id FROM uploads WHERE store='blob' AND committed_at=0 AND abandoned_at IS NULL AND expires_at<=? ORDER BY team,id LIMIT ?", tx.now().UnixNano(), limit)
		return err
	})
	for _, u := range expired {
		if err != nil {
			return err
		}
		err = o.s.inTx(ctx, func(tx *storeTx) error {
			if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
				return err
			}
			return abandonBlobUploadTx(ctx, tx, Team(u.team), u.key)
		})
	}
	return err
}

type teamKey struct{ team, key string }

func teamKeysTx(ctx context.Context, tx *storeTx, query string, args ...any) ([]teamKey, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var out []teamKey
	for rows.Next() {
		var b teamKey
		if err := rows.Scan(&b.team, &b.key); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		out = append(out, b)
	}
	return out, errors.Join(rows.Err(), rows.Close())
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
	var candidates []teamKey
	err := o.s.inTx(ctx, func(tx *storeTx) (err error) {
		if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
			return err
		}
		candidates, err = teamKeysTx(ctx, tx, "SELECT team,digest FROM blob_tombstones WHERE marked_at<=? ORDER BY team,digest LIMIT ?", tx.now().Add(-limits.BlobGCGrace).UnixNano(), limit)
		return err
	})
	if err != nil {
		return 0, err
	}
	var removed int64
	for _, c := range candidates {
		deleted, err := o.collectBlob(ctx, lease, blob.Blob{Team: c.team, Digest: blob.Digest(c.key)}, deleter)
		if err != nil {
			return removed, err
		}
		if deleted {
			removed++
		}
	}
	return removed, nil
}

// safety: collecting_at refuses publication until deletion finishes, but the final
// lease/tombstone check cannot fence a process paused inside a backend delete.
func (o *Operator) collectBlob(ctx context.Context, lease SingletonLease, b blob.Blob, deleter BlobDeleter) (bool, error) {
	var claimed bool
	var collectingAt int64
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		claimed = false
		if err := lockCollectedBlobTx(ctx, tx, lease, b); err != nil {
			return err
		}
		var marked, created int64
		var collecting sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT t.marked_at,t.collecting_at,b.size,b.created_at FROM blob_tombstones t JOIN blobs b ON b.team=t.team AND b.digest=t.digest WHERE t.team=? AND t.digest=?", b.Team, b.Digest).Scan(&marked, &collecting, &b.Size, &created)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || tx.now().Before(time.Unix(0, marked).Add(limits.BlobGCGrace)) {
			return err
		}
		reachable, err := blobReachableTx(ctx, tx, Team(b.Team), b.Digest)
		switch {
		case err != nil:
			return err
		case reachable && collecting.Valid:
			return fmt.Errorf("blob %s/%s gained a reference while its bytes were being collected", b.Team, b.Digest)
		case reachable:
			_, err = tx.ExecContext(ctx, "DELETE FROM blob_tombstones WHERE team=? AND digest=?", b.Team, b.Digest)
			return err
		}
		b.CreatedAt = time.Unix(0, created).UTC()
		claimed = true
		collectingAt = collecting.Int64
		if collecting.Valid {
			return nil
		}
		collectingAt = tx.now().UnixNano()
		_, err = tx.ExecContext(ctx, "UPDATE blob_tombstones SET collecting_at=? WHERE team=? AND digest=? AND collecting_at IS NULL", collectingAt, b.Team, b.Digest)
		return err
	})
	if err != nil || !claimed {
		return false, err
	}
	err = o.s.inTx(ctx, func(tx *storeTx) (err error) {
		claimed, err = blobCollectionCurrentTx(ctx, tx, lease, b, collectingAt)
		return err
	})
	if err != nil || !claimed {
		return false, err
	}
	if err := deleter.DeleteBlob(ctx, b); err != nil {
		return false, err
	}
	err = o.s.inTx(ctx, func(tx *storeTx) error {
		claimed = false
		current, err := blobCollectionCurrentTx(ctx, tx, lease, b, collectingAt)
		if err != nil || !current {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM blob_members WHERE team=? AND (manifest_digest=? OR member_digest=?)", b.Team, b.Digest, b.Digest); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM blobs WHERE team=? AND digest=? AND EXISTS(SELECT 1 FROM blob_tombstones t WHERE t.team=blobs.team AND t.digest=blobs.digest AND t.collecting_at IS NOT NULL)", b.Team, b.Digest)
		if err != nil {
			return err
		}
		if err := requireOneRow(result, "blob collection"); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed && err == nil, err
}

func blobCollectionCurrentTx(ctx context.Context, tx *storeTx, lease SingletonLease, b blob.Blob, collectingAt int64) (bool, error) {
	if err := lockCollectedBlobTx(ctx, tx, lease, b); err != nil {
		return false, err
	}
	var current int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM blob_tombstones WHERE team=? AND digest=? AND collecting_at=?", b.Team, b.Digest, collectingAt).Scan(&current)
	return current == 1, err
}

func lockCollectedBlobTx(ctx context.Context, tx *storeTx, lease SingletonLease, b blob.Blob) error {
	if err := assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now()); err != nil {
		return err
	}
	if err := advisoryLockTx(ctx, tx, "blob:"+b.Team+":"+string(b.Digest)); err != nil {
		return err
	}
	return assertSingletonLeaseTx(ctx, tx, lease, "maintenance", tx.now())
}

// safety: callers hold the digest's blob lock, which orders this check against collection.
func clearBlobTombstoneTx(ctx context.Context, tx *storeTx, team Team, digest blob.Digest) error {
	var collecting int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM blob_tombstones WHERE team=? AND digest=? AND collecting_at IS NOT NULL", team, digest).Scan(&collecting); err != nil {
		return err
	}
	if collecting != 0 {
		return authorityError(apierr.Conflict, "blob bytes are being collected; retry after collection finishes")
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM blob_tombstones WHERE team=? AND digest=?", team, digest)
	return err
}
