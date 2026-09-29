package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrUnknownPayment is returned when no paid grant carries the payment id a
// reversal or a freeze names.
var ErrUnknownPayment = errors.New("credits: no paid grant carries this payment id")

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

// ReversePayment takes back what a payment still has on the ledger: the paid
// grant carrying paymentID, less what earlier reversals of it took back, or
// amountMicro of it when that is positive and smaller, as a partial refund. It
// writes one reversal under reference, which makes the call idempotent: a
// repeat returns the reversal already written. The team is the one the
// payment funded, and the balance may go below zero, because the credits may
// already be spent; the claim path then refuses the team's new metered work.
func (s *Store) ReversePayment(
	ctx context.Context, paymentID, reference, createdBy string, amountMicro int64,
) (_ CreditReversal, err error) {
	paymentID, reference = strings.TrimSpace(paymentID), strings.TrimSpace(reference)
	if paymentID == "" || reference == "" {
		return CreditReversal{}, errors.New("credits: a reversal names the payment and its own reference")
	}
	id, err := newCreditID("grant")
	if err != nil {
		return CreditReversal{}, err
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return CreditReversal{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return CreditReversal{}, err
	}
	paid, found, err := creditGrantByReferenceTx(ctx, tx, "", CreditGrantPaid, paymentID)
	if err != nil {
		return CreditReversal{}, err
	}
	if !found {
		return CreditReversal{}, fmt.Errorf("%w: %q", ErrUnknownPayment, paymentID)
	}
	// safety: a lost dispute reverses under its dispute id, so a reference
	// that names a dispute held for another payment is that dispute misapplied
	// and must not take this payment back.
	if _, err := refuseBoundDisputeTx(ctx, tx, reference, paid.Team, paymentID); err != nil {
		return CreditReversal{}, err
	}
	out := CreditReversal{Team: paid.Team, PaidMicro: paid.AmountMicro}
	existing, found, err := creditGrantByReferenceTx(ctx, tx, paid.Team, CreditGrantReversal, reference)
	if err != nil {
		return CreditReversal{}, err
	}
	if found {
		if existing.Reverses != paymentID {
			return CreditReversal{}, fmt.Errorf("%w: %q reverses %q, not %q",
				ErrCreditGrantConflict, reference, existing.Reverses, paymentID)
		}
		out.Grant = &existing
	}
	already, err := reversedMicroTx(ctx, tx, paid.Team, paymentID)
	if err != nil {
		return CreditReversal{}, err
	}
	if out.Grant == nil && already < paid.AmountMicro {
		now := time.Now().UTC()
		take := paid.AmountMicro - already
		if amountMicro > 0 && amountMicro < take {
			take = amountMicro
		}
		grant := CreditGrant{
			ID: id, Team: paid.Team, Kind: CreditGrantReversal, AmountMicro: -take,
			Reference: reference, Reverses: paymentID, CreatedBy: createdBy, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			string(grant.Team), grant.ID, grant.Kind, grant.AmountMicro, grant.Reference, grant.Reverses,
			grant.CreatedBy, now.UnixNano()); err != nil {
			return CreditReversal{}, fmt.Errorf("credits: insert reversal: %w", err)
		}
		if err := recordGrantEventTx(tx, grant, "", ""); err != nil {
			return CreditReversal{}, err
		}
		out.Grant, out.Created = &grant, true
		already += take
	}
	out.ReversedMicro = already
	if out.BalanceMicro, err = creditBalanceTx(ctx, tx, paid.Team); err != nil {
		return CreditReversal{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreditReversal{}, err
	}
	if out.Created {
		s.invalidateRunnerCap()
	}
	return out, nil
}

// PaymentTeam returns the team a payment funded.
func (s *Store) PaymentTeam(ctx context.Context, paymentID string) (Team, error) {
	team, found, err := s.PaidGrantTeam(ctx, strings.TrimSpace(paymentID))
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%w: %q", ErrUnknownPayment, paymentID)
	}
	return team, nil
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

// HoldTeamForDispute holds team's cloud usage for disputeID, which disputes
// paymentID; an operator's hold by hand names no payment. A held team's
// metered claims are refused, so no new cloud work starts, while work
// already running finishes; the team stays held while any of its holds is
// unreleased. Holding a dispute that already has a hold for the same payment
// and team, released or not, writes nothing, so a replayed event never undoes
// an operator's release; one held for another payment or team is refused
// with [ErrDisputeConflict]. It reports whether it wrote the hold, and
// returns [ErrUnknownTeam] for a team that is not registered.
func (s *Store) HoldTeamForDispute(
	ctx context.Context, team Team, disputeID, paymentID, reason string, now time.Time,
) (bool, error) {
	return s.holdTeam(ctx, team, disputeID, paymentID, reason, "", now)
}

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
	_, err = t.s.holdTeam(ctx, t.team, id, "", reason, actor, now)
	return id, err
}

func (s *Store) holdTeam(
	ctx context.Context, team Team, disputeID, paymentID, reason, actor string, now time.Time,
) (_ bool, err error) {
	team = NormalizeTeam(team)
	disputeID, paymentID = strings.TrimSpace(disputeID), strings.TrimSpace(paymentID)
	if disputeID == "" {
		return false, errors.New("credits: a hold names the dispute it is for")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	// safety: a metered claim reads the freeze under the ledger lock, so the
	// hold takes the same lock; a claim that read the team as not frozen then
	// commits before the hold does, and none can commit after it.
	if err := lockCreditLedgerTx(ctx, tx); err != nil {
		return false, err
	}
	var registered int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams WHERE name = ?`, string(team)).Scan(&registered); err != nil {
		return false, err
	}
	if registered == 0 {
		return false, fmt.Errorf("%w: %s", ErrUnknownTeam, team)
	}
	bound, err := refuseBoundDisputeTx(ctx, tx, disputeID, team, paymentID)
	if err != nil || bound {
		return false, err
	}
	// safety: the ledger lock serializes holds, and the dispute has no row
	// yet, so the insert cannot meet a row another hold wrote.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credit_freezes (dispute_id, team, payment_id, reason, created_at) VALUES (?, ?, ?, ?, ?)`,
		disputeID, string(team), paymentID, truncate(reason, 500), now.UnixNano()); err != nil {
		return false, err
	}
	attrs := map[string]any{"payment_id": paymentID}
	// safety: a dispute's reason is the card holder's free text and stays off
	// the event; an operator's reason is the record of their decision.
	if actor != "" {
		attrs["reason"] = reason
	}
	if err := RecordBusinessEvent(tx, BusinessEvent{
		At: now, Team: team, Kind: BusinessEventTeamFrozen, SubjectID: disputeID, Actor: actor, Attrs: attrs,
	}); err != nil {
		return false, err
	}
	return true, tx.Commit()
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

// ReleaseCreditFreezes releases holds on team and reports how many it
// released: the one hold of disputeID, or every hold when disputeID is empty.
// [Store.DisputeTeam] finds the team of a dispute named alone.
func (s *Store) ReleaseCreditFreezes(ctx context.Context, team Team, disputeID string, now time.Time) (int64, error) {
	return s.releaseFreezes(ctx, team, disputeID, false, "", "", now)
}

// ReleaseOperatorHolds releases every hold an operator placed on t, and never
// a payment dispute's. Each release and its business event, naming actor and
// reason, commit together.
func (t *Tenant) ReleaseOperatorHolds(ctx context.Context, actor, reason string, now time.Time) (int64, error) {
	return t.s.releaseFreezes(ctx, t.team, "", true, actor, reason, now)
}

func (s *Store) releaseFreezes(
	ctx context.Context, team Team, disputeID string, operatorOnly bool, actor, reason string, now time.Time,
) (_ int64, err error) {
	team = NormalizeTeam(team)
	if team == "" {
		return 0, errors.New("credits: a release names the team")
	}
	query := `UPDATE credit_freezes SET released_at = ? WHERE team = ? AND released_at IS NULL`
	args := []any{now.UnixNano(), string(team)}
	if disputeID = strings.TrimSpace(disputeID); disputeID != "" {
		query, args = query+` AND dispute_id = ?`, append(args, disputeID)
	}
	if operatorOnly {
		query, args = query+` AND dispute_id LIKE ?`, append(args, OperatorHoldPrefix+"%")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackUnlessDone(tx, &err)
	released, err := releasedDisputesTx(ctx, tx, query+` RETURNING dispute_id`, args...)
	if err != nil {
		return 0, err
	}
	var releaseAttrs map[string]any
	if actor != "" {
		releaseAttrs = map[string]any{"reason": reason}
	}
	for _, id := range released {
		if err := RecordBusinessEvent(tx, BusinessEvent{
			At: now, Team: team, Kind: BusinessEventTeamUnfrozen, SubjectID: id, Actor: actor, Attrs: releaseAttrs,
		}); err != nil {
			return 0, err
		}
	}
	return int64(len(released)), tx.Commit()
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

// DisputeTeam returns the team a dispute's hold is on.
func (s *Store) DisputeTeam(ctx context.Context, disputeID string) (Team, bool, error) {
	var team string
	err := s.queryRow(ctx, `SELECT team FROM credit_freezes WHERE dispute_id = ? LIMIT 1`,
		strings.TrimSpace(disputeID)).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return Team(team), err == nil, err
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
