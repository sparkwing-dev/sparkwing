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
)

// GitHubAppDeliveryRetention is how long a completed or failed delivery is bound to
// its event and delivery ID. Pending work is retained until it finishes.
const GitHubAppDeliveryRetention = 90 * 24 * time.Hour

var (
	// ErrGitHubDeliveryConflict means a delivery ID or payload digest was
	// previously bound to different signed material or an event name.
	ErrGitHubDeliveryConflict = errors.New("store: GitHub delivery binding conflicts")
	// errGitHubDeliveryNotProcessing fences completion and lifecycle writes to the
	// delivery the controller's inbox worker is processing.
	errGitHubDeliveryNotProcessing = errors.New("store: GitHub delivery is not being processed")
)

// GitHubDelivery is one durable App webhook claimed by the controller's single inbox worker. Its processing state
// fences mutations and completion. Processing failures require an administrator to enqueue another claim.
type GitHubDelivery struct {
	ID         string
	Event      string
	Body       []byte
	ReceivedAt time.Time
}

type githubDeliveryContextKey struct{}

// Context binds mutations to this claimed delivery. Each mutation's
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
	_, err = tx.execContext(ctx, `INSERT INTO github_app_deliveries
		(digest,delivery_id,received_at,event,body,state,queue_team,installation_id,connection_generation)
		VALUES (?,?,?,?,?,'pending',?,?,?)
		ON CONFLICT DO NOTHING`, digest, deliveryID, now.UnixNano(), event, body, queueTeam, installation, generation)
	if err != nil {
		return err
	}
	_, err = tx.execContext(ctx, `INSERT INTO github_app_delivery_ids (delivery_id,digest) VALUES (?,?) ON CONFLICT DO NOTHING`, deliveryID, digest)
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

// ClaimGitHubDelivery rotates across teams and claims the team's oldest ready delivery. The one inbox worker
// claims only after finishing its previous delivery, so a delivery still processing was interrupted by a restart or a
// failed finish; it fails and requires manual processing again.
func (o *Operator) ClaimGitHubDelivery(ctx context.Context) (*GitHubDelivery, error) {
	tx, err := o.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	const cursorKey = "github_delivery_claim_team"
	if _, err := tx.execContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO NOTHING`, cursorKey, []byte(`""`)); err != nil {
		return nil, err
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`+tx.forUpdate(), cursorKey).Scan(&raw); err != nil {
		return nil, err
	}
	if _, err := tx.execContext(ctx, `UPDATE github_app_deliveries SET state='failed',last_error='processing was interrupted' WHERE state='processing'`); err != nil {
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
	err = tx.QueryRowContext(ctx, `SELECT digest,delivery_id,event,body,received_at
		FROM github_app_deliveries WHERE queue_team=? AND state='pending' ORDER BY received_at,digest LIMIT 1`, team).
		Scan(&digest, &d.ID, &d.Event, &d.Body, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.execContext(ctx, `UPDATE github_app_deliveries SET state='processing' WHERE digest=? AND state='pending'`, digest); err != nil {
		return nil, err
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
	d.ReceivedAt = time.Unix(0, receivedAt).UTC()
	return &d, nil
}

func githubDeliveryClaimTx(ctx context.Context, tx *storeTx) (string, error) {
	delivery, ok := ctx.Value(githubDeliveryContextKey{}).(GitHubDelivery)
	if !ok {
		return "", nil
	}
	installation := githubDeliveryInstallation(delivery.Body)
	if installation != 0 {
		if err := acquireLockSet(ctx, tx, func() lockSet { return tx.operator.discoverInstallationLocks(ctx, tx, lockSet{}, installation) }); err != nil {
			return "", err
		}
	}
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT digest FROM github_app_deliveries WHERE delivery_id=? AND state='processing'`+tx.forUpdate(), delivery.ID).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errGitHubDeliveryNotProcessing
	}
	if err != nil {
		return "", err
	}
	return digest, nil
}

// githubDeliveryFenceTx checks the delivery is still processing and the connection captured at receipt.
func githubDeliveryFenceTx(ctx context.Context, tx *storeTx) (string, error) {
	digest, err := githubDeliveryClaimTx(ctx, tx)
	if err != nil || digest == "" {
		return digest, err
	}
	return digest, githubDeliveryConnectionTx(ctx, tx, digest)
}

func githubDeliveryResultTx[T any](ctx context.Context, tx *storeTx, effect string, apply func() (T, error)) (T, error) {
	var result T
	if effect == "" {
		return result, ErrInvalidInput
	}
	digest, err := githubDeliveryClaimTx(ctx, tx)
	if err != nil {
		return result, err
	}
	if digest == "" {
		return apply()
	}
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT result FROM github_app_delivery_effects WHERE digest=? AND effect=?`, digest, effect).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if err := githubDeliveryConnectionTx(ctx, tx, digest); err != nil {
		return result, err
	}
	result, err = apply()
	if err != nil {
		return result, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	_, err = tx.execContext(ctx, `INSERT INTO github_app_delivery_effects(digest,effect,result) VALUES (?,?,?)`, digest, effect, string(encoded))
	return result, err
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
		return errors.Join(bindingRefusal(), ErrGitHubAppAutomationRevoked)
	}
	return err
}

func (s *Store) githubDeliveryEffect(ctx context.Context, effect string, apply func(*storeTx) error) error {
	if effect == "" {
		return ErrInvalidInput
	}
	return s.inTx(ctx, func(tx *storeTx) error {
		_, err := githubDeliveryResultTx(ctx, tx, "lifecycle:"+effect, func() (struct{}, error) { return struct{}{}, apply(tx) })
		return err
	})
}

func (s *Store) githubDeliveryExec(ctx context.Context, effect, query string, args ...any) (sql.Result, error) {
	if effect == "" {
		return nil, ErrInvalidInput
	}
	affected, err := inTxResult(ctx, s, func(tx *storeTx) (int64, error) {
		return githubDeliveryResultTx(ctx, tx, "lifecycle:"+effect, func() (int64, error) {
			result, err := tx.execContext(ctx, query, args...)
			if err != nil {
				return 0, err
			}
			return result.RowsAffected()
		})
	})
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(affected), nil
}

// FinishGitHubDelivery records the processing delivery's success or failure. Failures retain the
// signed body for an administrator's explicit process-again action.
func (o *Operator) FinishGitHubDelivery(ctx context.Context, claimed GitHubDelivery, reason string) error {
	if claimed.ID == "" {
		return ErrInvalidInput
	}
	state := "done"
	if reason != "" {
		state = "failed"
	}
	return o.s.inTx(ctx, func(tx *storeTx) error {
		result, err := tx.execContext(ctx, `UPDATE github_app_deliveries SET state=?,
			body=CASE WHEN ?='done' THEN NULL ELSE body END,last_error=?
			WHERE delivery_id=? AND state='processing'`, state, state, reason, claimed.ID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errGitHubDeliveryNotProcessing
		}
		return nil
	})
}

// ListFailedGitHubDeliveries exposes processing failures under current team administration.
func (t *Tenant) ListFailedGitHubDeliveries(ctx context.Context, a capability.Authorized) ([]FailedGitHubDelivery, error) {
	out := []FailedGitHubDelivery{}
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		if err := t.credentialAdminTx(ctx, tx, a); err != nil {
			return err
		}
		rows, err := tx.queryContext(ctx, `SELECT delivery_id,event,received_at,last_error FROM github_app_deliveries WHERE queue_team=? AND state='failed' ORDER BY received_at DESC,digest`, t.team)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d FailedGitHubDelivery
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
		return t.teamMutationInTx(ctx, tx, a, "ProcessGitHubDeliveryAgain", deliveryID, func(tx *storeTx, _ capability.Principal) error {
			if err := githubDeliveryConnectionTx(ctx, tx, digest); err != nil {
				return err
			}
			result, err := tx.execContext(ctx, `UPDATE github_app_deliveries SET state='pending' WHERE delivery_id=? AND queue_team=? AND state='failed'`, deliveryID, t.team)
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
			return nil
		})
	})
}
