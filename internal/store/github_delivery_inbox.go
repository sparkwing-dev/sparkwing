package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GitHubAppDeliveryRetention is how long a completed delivery remains bound to
// its event and delivery ID. Pending work is retained until it finishes.
const GitHubAppDeliveryRetention = 90 * 24 * time.Hour

const githubDeliveryLease = 2 * time.Minute

var (
	// ErrGitHubDeliveryConflict means a delivery ID or payload digest was
	// previously bound to different signed material or an event name.
	ErrGitHubDeliveryConflict = errors.New("store: GitHub delivery binding conflicts")
	// ErrGitHubDeliveryLeaseLost means a worker tried to finish a claim that
	// expired or was replaced by a later claim.
	ErrGitHubDeliveryLeaseLost = errors.New("store: GitHub delivery lease lost")
)

// GitHubDelivery is one durable, leased App webhook. Lease fences Finish calls;
// Attempts counts claims, including claims recovered after a crash.
type GitHubDelivery struct {
	ID         string
	Event      string
	Body       []byte
	Lease      int64
	Attempts   int64
	ReceivedAt time.Time
	LastError  string
}

// EnqueueGitHubDelivery durably binds the delivery ID and the SHA-256 digest
// of body to event. Replaying an identical delivery or body is idempotent.
func (o *Operator) EnqueueGitHubDelivery(ctx context.Context, deliveryID, event string, body []byte, now time.Time) error {
	if deliveryID == "" || event == "" || body == nil {
		return ErrInvalidInput
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	_, err = tx.ExecContext(ctx, `INSERT INTO github_app_deliveries
		(digest,delivery_id,received_at,event,body,state,available_at)
		VALUES (?,?,?,?,?,'pending',?) ON CONFLICT DO NOTHING`, digest, deliveryID, now.UnixNano(), event, body, now.UnixNano())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO github_app_delivery_ids (delivery_id,digest) VALUES (?,?) ON CONFLICT DO NOTHING`, deliveryID, digest)
	if err != nil {
		return err
	}
	var boundDigest, boundEvent string
	if err := tx.QueryRowContext(ctx, `SELECT digest FROM github_app_delivery_ids WHERE delivery_id=?`, deliveryID).Scan(&boundDigest); err != nil {
		return err
	}
	if boundDigest != digest {
		return ErrGitHubDeliveryConflict
	}
	err = tx.QueryRowContext(ctx, `SELECT event FROM github_app_deliveries WHERE digest=?`, digest).Scan(&boundEvent)
	if err != nil {
		return err
	}
	if boundEvent != event {
		return ErrGitHubDeliveryConflict
	}
	return tx.Commit()
}

// ClaimGitHubDelivery leases the oldest ready or expired delivery. It returns
// nil when no delivery is ready. An expired lease is claimable after restart.
func (o *Operator) ClaimGitHubDelivery(ctx context.Context, now time.Time) (*GitHubDelivery, error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	var digest string
	var d GitHubDelivery
	var receivedAt int64
	err = tx.QueryRowContext(ctx, `SELECT digest,delivery_id,event,body,lease,attempts,received_at,last_error
		FROM github_app_deliveries WHERE (state='pending' AND available_at<=?)
		OR (state='leased' AND lease_until<=?) ORDER BY received_at,digest LIMIT 1`, now.UnixNano(), now.UnixNano()).
		Scan(&digest, &d.ID, &d.Event, &d.Body, &d.Lease, &d.Attempts, &receivedAt, &d.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_app_deliveries SET state='leased',lease=lease+1,
		attempts=attempts+1,lease_until=? WHERE digest=? AND lease=? AND
		((state='pending' AND available_at<=?) OR (state='leased' AND lease_until<=?))`,
		now.Add(githubDeliveryLease).UnixNano(), digest, d.Lease, now.UnixNano(), now.UnixNano())
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrGitHubDeliveryLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	d.Lease++
	d.Attempts++
	d.ReceivedAt = time.Unix(0, receivedAt).UTC()
	return &d, nil
}

// FinishGitHubDelivery completes a current claim when retryReason is empty.
// Otherwise it keeps the body and schedules another claim with bounded delay.
func (o *Operator) FinishGitHubDelivery(ctx context.Context, claimed GitHubDelivery, retryReason string, now time.Time) error {
	if claimed.ID == "" || claimed.Lease <= 0 {
		return ErrInvalidInput
	}
	var result sql.Result
	var err error
	if retryReason == "" {
		result, err = o.s.exec(ctx, `UPDATE github_app_deliveries SET state='done',body=NULL,
			lease_until=0,last_error='' WHERE delivery_id=? AND state='leased' AND lease=? AND lease_until>?`,
			claimed.ID, claimed.Lease, now.UnixNano())
	} else {
		attempt := claimed.Attempts
		if attempt < 1 {
			attempt = 1
		}
		if attempt > 10 {
			attempt = 10
		}
		delay := time.Second * time.Duration(1<<uint(attempt-1))
		result, err = o.s.exec(ctx, `UPDATE github_app_deliveries SET state='pending',
			available_at=?,lease_until=0,last_error=? WHERE delivery_id=? AND state='leased' AND lease=? AND lease_until>?`,
			now.Add(delay).UnixNano(), retryReason, claimed.ID, claimed.Lease, now.UnixNano())
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrGitHubDeliveryLeaseLost
	}
	return nil
}
