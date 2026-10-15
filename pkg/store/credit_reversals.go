package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUnknownPayment is returned when a provider payment has no settled grant.
var ErrUnknownPayment = errors.New("credits: no settled grant belongs to this payment id")

// CreditReversal reports what reversing a payment did. Grant is the reversal
// the call wrote, or the one an earlier call wrote under the same reference;
// it is nil when the payment's reversals already took back all it paid.
type CreditReversal struct {
	Team          Team
	PaidMicro     int64
	ReversedMicro int64
	BalanceMicro  int64
	Grant         *CreditGrant
	Created       bool
}

// safety: a refund returns a payment's unapplied rest first, released here up
// to amountMicro, so its reversal takes back only what it returns beyond it.
func releaseUnappliedTx(
	ctx context.Context, tx *storeTx, team Team, paymentID, reference, createdBy string, amountMicro int64,
) (int64, error) {
	var held sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT SUM(amount_micro) FROM credit_grants
	    WHERE team = ? AND kind = ? AND reverses = ? AND reference LIKE ?`,
		string(team), CreditGrantReversal, paymentID, unappliedPrefix+"%").Scan(&held); err != nil {
		return 0, err
	}
	release := min(amountMicro, -held.Int64)
	if release <= 0 {
		return 0, nil
	}
	id, err := newCreditID("grant")
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses,
	    created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(team), id, CreditGrantReversal, release, unappliedPrefix+"refund:"+reference, paymentID, createdBy,
		tx.now().UnixNano()); err != nil {
		return 0, fmt.Errorf("credits: release unapplied payment: %w", err)
	}
	return release, nil
}

// TeamCreditFreeze is whether a team's cloud usage is held, and by which
// disputes.
type TeamCreditFreeze struct {
	Frozen   bool
	Disputes []string
}

// ErrDisputeConflict is returned when a hold or a reversal names a dispute
// already held for another payment or team: a dispute disputes one payment.
var ErrDisputeConflict = errors.New("credits: the dispute is already held for another payment")

// OperatorHoldPrefix begins the id of every hold an operator places by hand,
// which keeps those holds apart from a payment dispute's.
const OperatorHoldPrefix = "operator-"

// HoldByOperator holds t on actor's word and returns the hold's id. The hold
// and its business event, naming actor and reason, commit together.
func (t *Tenant) HoldByOperator(ctx context.Context, actor, reason string, now time.Time) (string, error) {
	id, err := newCreditID("hold")
	if err != nil {
		return "", err
	}
	id = OperatorHoldPrefix + id
	_, err = t.hold(ctx, id, "", reason, actor, now)
	return id, err
}

func (t *Tenant) hold(ctx context.Context, disputeID, paymentID, reason, actor string, now time.Time) (created bool, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		created, err = holdCreditFreezeTx(ctx, tx, t.team, disputeID, paymentID, reason, actor, now)
		return err
	})
	if err != nil {
		return false, err
	}
	return created, err
}

func refuseBoundDisputeTx(ctx context.Context, tx *storeTx, disputeID string, team Team, paymentID string) (bool, error) {
	heldTeam, heldPayment, found, err := disputeHoldTx(ctx, tx, disputeID)
	if err != nil || !found {
		return false, err
	}
	if heldTeam != team || heldPayment != paymentID {
		return true, fmt.Errorf("%w: %s is held for payment %q of team %s, not payment %q of team %s",
			ErrDisputeConflict, disputeID, heldPayment, heldTeam, paymentID, team)
	}
	return true, nil
}

func disputeHoldTx(ctx context.Context, tx *storeTx, disputeID string) (Team, string, bool, error) {
	var team, payment string
	err := tx.QueryRowContext(ctx, `SELECT team, payment_id FROM credit_freezes WHERE dispute_id = ?`,
		disputeID).Scan(&team, &payment)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return Team(team), payment, err == nil, err
}

// ReleaseCreditFreezes releases the named hold on this tenant and commits its audit event.
func (t *Tenant) ReleaseCreditFreezes(ctx context.Context, disputeID, actor, reason string, now time.Time) (int64, error) {
	if strings.TrimSpace(disputeID) == "" {
		return 0, fmt.Errorf("%w: a release names the hold it releases", ErrInvalidInput)
	}
	return t.releaseFreezes(ctx, disputeID, false, actor, reason, now)
}

// ReleaseOperatorHolds releases every hold an operator placed on t, and never
// a payment dispute's. Each release and its business event, naming actor and
// reason, commit together.
func (t *Tenant) ReleaseOperatorHolds(ctx context.Context, actor, reason string, now time.Time) (int64, error) {
	return t.releaseFreezes(ctx, "", true, actor, reason, now)
}

func (t *Tenant) releaseFreezes(ctx context.Context, disputeID string, operatorOnly bool, actor, reason string, now time.Time) (count int64, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		count, err = releaseCreditFreezesTx(ctx, tx, t.team, disputeID, operatorOnly, actor, reason, now)
		return err
	})
	if err != nil {
		return 0, err
	}
	return count, err
}

func releasedDisputesTx(ctx context.Context, tx *storeTx, query string, args ...any) (_ []string, err error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var released []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		released = append(released, id)
	}
	return released, rows.Err()
}

// CreditFreeze reports whether t's cloud usage is held.
func (t *Tenant) CreditFreeze(ctx context.Context) (_ TeamCreditFreeze, err error) {
	rows, err := t.s.query(ctx, `SELECT dispute_id FROM credit_freezes
	  WHERE team = ? AND released_at IS NULL ORDER BY created_at, dispute_id`, string(t.team))
	if err != nil {
		return TeamCreditFreeze{}, err
	}
	defer closeRowsInto(rows, &err)
	return scanCreditFreeze(rows)
}

func teamCreditFreezeTx(ctx context.Context, tx *storeTx, team Team) (_ TeamCreditFreeze, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT dispute_id FROM credit_freezes
	  WHERE team = ? AND released_at IS NULL ORDER BY created_at, dispute_id`, string(team))
	if err != nil {
		return TeamCreditFreeze{}, err
	}
	defer closeRowsInto(rows, &err)
	return scanCreditFreeze(rows)
}

func scanCreditFreeze(rows *sql.Rows) (TeamCreditFreeze, error) {
	var out TeamCreditFreeze
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return TeamCreditFreeze{}, err
		}
		out.Disputes = append(out.Disputes, id)
	}
	out.Frozen = len(out.Disputes) > 0
	return out, rows.Err()
}
