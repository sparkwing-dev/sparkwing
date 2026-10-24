package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// errUnknownPayment is returned when a provider payment has no settled grant.
var errUnknownPayment = errors.New("credits: no settled grant belongs to this payment id")

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

// TeamCreditFreeze lists a team's open holds, named by id in Disputes, with each hold's cause in Causes.
type TeamCreditFreeze struct {
	Frozen   bool
	Disputes []string
	Causes   []string
}

// Hold causes. Every hold blocks new Cloud admissions and the renewal of running Cloud work alike; the cause only
// names who can lift it.
const (
	HoldCauseOperator = "operator"
	holdCauseDispute  = "dispute"
	HoldCauseWarning  = "early_fraud_warning"
	holdCauseRevoked  = "revoked"
)

// safety: the one reader of whether a team is held: revoked standing, or else its oldest open hold.
func teamHoldTx(ctx context.Context, q rowQuerier, team Team) (string, error) {
	var trust string
	if err := q.QueryRowContext(ctx, `SELECT billing_trust FROM teams WHERE name = ?`, string(team)).Scan(&trust); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if trust == billingTrustRevoked {
		return holdCauseRevoked, nil
	}
	var cause string
	err := q.QueryRowContext(ctx, `SELECT cause FROM credit_freezes WHERE team = ? AND released_at IS NULL
	    ORDER BY created_at, dispute_id LIMIT 1`, string(team)).Scan(&cause)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return cause, err
}

// errDisputeConflict is returned when a hold or a reversal names a dispute
// already held for another payment or team: a dispute disputes one payment.
var errDisputeConflict = errors.New("credits: the dispute is already held for another payment")

// operatorHoldPrefix begins the id of every hold an operator places by hand,
// which keeps those holds apart from a payment dispute's.
const operatorHoldPrefix = "operator-"

// HoldByOperator holds t on actor's word and returns the hold's id. The hold
// and its audit fact, naming actor and hold ID, commit together.
func (t *Tenant) HoldByOperator(ctx context.Context, actor, reason string) (string, error) {
	id, err := newCreditID("hold")
	if err != nil {
		return "", err
	}
	id = operatorHoldPrefix + id
	_, err = t.hold(ctx, id, "", reason, actor)
	return id, err
}

func (t *Tenant) hold(ctx context.Context, disputeID, paymentID, reason, actor string) (created bool, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		created, err = holdCreditFreezeTx(ctx, tx, t.team, disputeID, paymentID, reason, actor, tx.now(ctx), auditActor{system: auditBreakGlass})
		return err
	})
	if err != nil {
		return false, err
	}
	return created, err
}

func refuseBoundDisputeTx(ctx context.Context, tx *storeTx, disputeID string, team Team, paymentID string) (bool, error) {
	heldTeam, heldPayment, found, err := tx.operator.disputeHoldTx(ctx, tx, disputeID)
	if err != nil || !found {
		return false, err
	}
	if heldTeam != team || heldPayment != paymentID {
		return true, fmt.Errorf("%w: %s is held for payment %q of team %s, not payment %q of team %s",
			errDisputeConflict, disputeID, heldPayment, heldTeam, paymentID, team)
	}
	return true, nil
}

func (op *Operator) disputeHoldTx(ctx context.Context, tx *storeTx, disputeID string) (Team, string, bool, error) {
	var team, payment string
	err := tx.QueryRowContext(ctx, `SELECT team, payment_id FROM credit_freezes WHERE dispute_id = ?`,
		disputeID).Scan(&team, &payment)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return Team(team), payment, err == nil, err
}

// ReleaseCreditFreezes releases the named hold on this tenant and commits its audit event.
func (t *Tenant) ReleaseCreditFreezes(ctx context.Context, disputeID, actor, reason string) (int64, error) {
	if strings.TrimSpace(disputeID) == "" {
		return 0, fmt.Errorf("%w: a release names the hold it releases", ErrInvalidInput)
	}
	return t.releaseFreezes(ctx, disputeID, false, actor, reason)
}

// ReleaseOperatorHolds releases every hold an operator placed on t, and never
// a payment dispute's. Each release and its audit fact, naming actor and hold
// ID, commit together.
func (t *Tenant) ReleaseOperatorHolds(ctx context.Context, actor, reason string) (int64, error) {
	return t.releaseFreezes(ctx, "", true, actor, reason)
}

func (t *Tenant) releaseFreezes(ctx context.Context, disputeID string, operatorOnly bool, actor, reason string) (count int64, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		count, err = releaseCreditFreezesTx(ctx, tx, t.team, disputeID, operatorOnly, false, actor, reason, tx.now(ctx), auditActor{system: auditBreakGlass})
		return err
	})
	if err != nil {
		return 0, err
	}
	return count, err
}

func releasedDisputesTx(ctx context.Context, tx *storeTx, query string, args ...any) (_ []string, err error) {
	rows, err := tx.queryContext(ctx, query, args...)
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
	tx, err := t.s.beginSnapshotReadTx(ctx)
	if err != nil {
		return TeamCreditFreeze{}, err
	}
	defer rollbackOrLog(tx)
	return teamCreditFreezeTx(ctx, tx, t.team)
}

func teamCreditFreezeTx(ctx context.Context, tx *storeTx, team Team) (_ TeamCreditFreeze, err error) {
	cause, err := teamHoldTx(ctx, tx, team)
	if err != nil {
		return TeamCreditFreeze{}, err
	}
	rows, err := tx.queryContext(ctx, `SELECT dispute_id, cause FROM credit_freezes
	  WHERE team = ? AND released_at IS NULL ORDER BY created_at, dispute_id`, string(team))
	if err != nil {
		return TeamCreditFreeze{}, err
	}
	defer closeRowsInto(rows, &err)
	out, err := scanCreditFreeze(rows)
	out.Frozen = cause != ""
	return out, err
}

func scanCreditFreeze(rows rowCursor) (TeamCreditFreeze, error) {
	var out TeamCreditFreeze
	for rows.Next() {
		var id, cause string
		if err := rows.Scan(&id, &cause); err != nil {
			return TeamCreditFreeze{}, err
		}
		out.Disputes = append(out.Disputes, id)
		out.Causes = append(out.Causes, cause)
	}
	return out, rows.Err()
}
