package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// GitHubAppDeliveryRetention is how long a completed or failed delivery is bound to
// its event and delivery ID. Pending work is retained until it finishes.
const GitHubAppDeliveryRetention = 90 * 24 * time.Hour

const githubDeliveryLease = 2 * time.Minute

var (
	// ErrGitHubDeliveryConflict means a delivery ID or payload digest was
	// previously bound to different signed material or an event name.
	ErrGitHubDeliveryConflict = errors.New("store: GitHub delivery binding conflicts")
	// errGitHubDeliveryLeaseLost fences completion and lifecycle writes from
	// a worker whose claim expired or was replaced.
	errGitHubDeliveryLeaseLost = errors.New("store: GitHub delivery lease lost")
)

// GitHubDelivery is one durable, leased App webhook. Its lease fences mutations
// and completion. Processing failures require an administrator to enqueue another claim.
type GitHubDelivery struct {
	ID         string
	Event      string
	Body       []byte
	Lease      int64
	ReceivedAt time.Time
}

type githubDeliveryContextKey struct{}

// Context binds mutations to this leased delivery. Each mutation's
// receipt commits with its effect so manual replay cannot overwrite later events.
func (d GitHubDelivery) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, githubDeliveryContextKey{}, d)
}

func githubDeliveryInstallation(body []byte) int64 {
	var envelope struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return 0
	}
	return envelope.Installation.ID
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
	installation := githubDeliveryInstallation(body)
	if err := acquireLockSet(ctx, tx, func() lockSet { return tx.operator.discoverInstallationLocks(ctx, tx, lockSet{}, installation) }); err != nil {
		return err
	}
	queueTeam, generation, err := tx.operator.githubInstallationConnectionTx(ctx, tx, installation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO github_app_deliveries
		(digest,delivery_id,received_at,event,body,state,queue_team,installation_id,connection_generation)
		VALUES (?,?,?,?,?,'pending',?,?,?)
		ON CONFLICT DO NOTHING`, digest, deliveryID, now.UnixNano(), event, body, queueTeam, installation, generation)
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

// ClaimGitHubDelivery rotates across teams and leases the team's oldest ready
// delivery. Expired claims fail and require manual processing again.
func (o *Operator) ClaimGitHubDelivery(ctx context.Context, now time.Time) (*GitHubDelivery, error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	const cursorKey = "github_delivery_claim_team"
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO NOTHING`, cursorKey, []byte(`""`)); err != nil {
		return nil, err
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`+tx.forUpdate(), cursorKey).Scan(&raw); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE github_app_deliveries SET state='failed',lease_until=0,last_error='processing lease expired' WHERE state='leased' AND lease_until<=?`, now.UnixNano()); err != nil {
		return nil, err
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, err
	}
	var team string
	err = tx.QueryRowContext(ctx, `SELECT queue_team FROM github_app_deliveries
		WHERE state='pending'
		GROUP BY queue_team ORDER BY CASE WHEN queue_team>? THEN 0 ELSE 1 END,queue_team LIMIT 1`,
		cursor).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	var digest string
	var d GitHubDelivery
	var receivedAt int64
	err = tx.QueryRowContext(ctx, `SELECT digest,delivery_id,event,body,lease,received_at
		FROM github_app_deliveries WHERE queue_team=? AND state='pending' ORDER BY received_at,digest LIMIT 1`, team).
		Scan(&digest, &d.ID, &d.Event, &d.Body, &d.Lease, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE github_app_deliveries SET state='leased',lease=lease+1,
		lease_until=? WHERE digest=? AND lease=? AND state='pending'`,
		now.Add(githubDeliveryLease).UnixNano(), digest, d.Lease)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, errGitHubDeliveryLeaseLost
	}
	value, err := json.Marshal(team)
	if err != nil {
		return nil, err
	}
	if err := setCreditSettingTx(ctx, tx, cursorKey, string(value)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	d.Lease++
	d.ReceivedAt = time.Unix(0, receivedAt).UTC()
	return &d, nil
}

// githubDeliveryMutationTx fences every inbox mutation to its live claim and
// original connection. Lifecycle receipts make applied effects survive manual replay.
func githubDeliveryMutationTx(ctx context.Context, tx *storeTx, effect string) (bool, error) {
	delivery, ok := ctx.Value(githubDeliveryContextKey{}).(GitHubDelivery)
	if !ok {
		return true, nil
	}
	installation := githubDeliveryInstallation(delivery.Body)
	if installation != 0 {
		if err := acquireLockSet(ctx, tx, func() lockSet { return tx.operator.discoverInstallationLocks(ctx, tx, lockSet{}, installation) }); err != nil {
			return false, err
		}
	}
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT digest FROM github_app_deliveries
		WHERE delivery_id=? AND state='leased' AND lease=? AND lease_until>?`+tx.forUpdate(),
		delivery.ID, delivery.Lease, tx.now().UnixNano()).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errGitHubDeliveryLeaseLost
	}
	if err != nil {
		return false, err
	}
	if err := githubDeliveryConnectionTx(ctx, tx, digest); err != nil {
		return false, err
	}
	if effect != "" {
		result, err := tx.ExecContext(ctx, `INSERT INTO github_app_delivery_effects(digest,effect)
			VALUES (?,?) ON CONFLICT DO NOTHING`, digest, effect)
		if err != nil {
			return false, err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return false, err
		}
	}
	return true, nil
}

// A receipt belongs to one connection, even when an installation returns to its former team.
func githubDeliveryConnectionTx(ctx context.Context, tx *storeTx, digest string) error {
	var installation int64
	var receivedGeneration string
	if err := tx.QueryRowContext(ctx, `SELECT installation_id,connection_generation FROM github_app_deliveries WHERE digest=?`, digest).Scan(&installation, &receivedGeneration); err != nil {
		return err
	}
	if installation == 0 {
		return nil
	}
	if err := acquireLockSet(ctx, tx, func() lockSet { return tx.operator.discoverInstallationLocks(ctx, tx, lockSet{}, installation) }); err != nil {
		return err
	}
	_, generation, err := tx.operator.githubInstallationConnectionTx(ctx, tx, installation)
	if errors.Is(err, sql.ErrNoRows) || err == nil && generation != receivedGeneration {
		return bindingRefusal()
	}
	return err
}

func (s *Store) githubDeliveryEffect(ctx context.Context, effect string, apply func(*storeTx) error) error {
	return s.inTx(ctx, func(tx *storeTx) error {
		applyEffect, err := githubDeliveryMutationTx(ctx, tx, effect)
		if err != nil || !applyEffect {
			return err
		}
		return apply(tx)
	})
}

func (s *Store) githubDeliveryExec(ctx context.Context, effect, query string, args ...any) (sql.Result, error) {
	var result sql.Result = driver.RowsAffected(0)
	err := s.githubDeliveryEffect(ctx, effect, func(tx *storeTx) (err error) {
		result, err = tx.ExecContext(ctx, query, args...)
		return err
	})
	return result, err
}

// FinishGitHubDelivery records a fenced success or failure. Failures retain the
// signed body for an administrator's explicit process-again action.
func (o *Operator) FinishGitHubDelivery(ctx context.Context, claimed GitHubDelivery, reason string, now time.Time) error {
	if claimed.ID == "" || claimed.Lease <= 0 {
		return ErrInvalidInput
	}
	state := "done"
	if reason != "" {
		state = "failed"
	}
	result, err := o.s.exec(ctx, `UPDATE github_app_deliveries SET state=?,
		body=CASE WHEN ?='done' THEN NULL ELSE body END,lease_until=0,last_error=?
		WHERE delivery_id=? AND state='leased' AND lease=? AND lease_until>?`, state, state, reason, claimed.ID, claimed.Lease, now.UnixNano())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errGitHubDeliveryLeaseLost
	}
	return nil
}

// ListFailedGitHubDeliveries exposes processing failures under current team administration.
func (t *Tenant) ListFailedGitHubDeliveries(ctx context.Context, a capability.Authorized) ([]controllerwire.FailedGitHubDelivery, error) {
	out := []controllerwire.FailedGitHubDelivery{}
	err := t.teamMutationTx(ctx, a, func(tx *storeTx, _ capability.Principal) error {
		rows, err := tx.QueryContext(ctx, `SELECT delivery_id,event,received_at,last_error FROM github_app_deliveries WHERE queue_team=? AND state='failed' ORDER BY received_at DESC,digest`, t.team)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d controllerwire.FailedGitHubDelivery
			var received int64
			if err := rows.Scan(&d.ID, &d.Event, &received, &d.LastError); err != nil {
				return err
			}
			d.ReceivedAt = time.Unix(0, received).UTC()
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// ProcessGitHubDeliveryAgain queues one failed delivery and its audit atomically.
func (t *Tenant) ProcessGitHubDeliveryAgain(ctx context.Context, a capability.Authorized, deliveryID string) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		var digest string
		var installation int64
		if err := tx.QueryRowContext(ctx, `SELECT digest,installation_id FROM github_app_deliveries WHERE delivery_id=? AND queue_team=? AND state='failed'`, deliveryID, t.team).Scan(&digest, &installation); err != nil {
			return err
		}
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			set.add(lockTeam, true, string(t.team))
			return tx.operator.discoverInstallationLocks(ctx, tx, set, installation)
		}); err != nil {
			return err
		}
		return t.teamMutationInTx(ctx, tx, a, func(tx *storeTx, p capability.Principal) error {
			if err := githubDeliveryConnectionTx(ctx, tx, digest); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `UPDATE github_app_deliveries SET state='pending' WHERE delivery_id=? AND queue_team=? AND state='failed'`, deliveryID, t.team)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return sql.ErrNoRows
			}
			return tx.operator.RecordBusinessEvent(tx, BusinessEvent{At: tx.now(), Team: t.team, Kind: "github.delivery_reprocessed", SubjectID: deliveryID, Actor: p.Capability.Subject.ID})
		})
	})
}
