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
// grant carrying paymentID, less what earlier reversals of it took back. It
// writes one reversal under reference, which makes the call idempotent: a
// repeat returns the reversal already written. The team is the one the
// payment funded, and the balance may go below zero, because the credits may
// already be spent; the claim path then refuses the team's new metered work.
func (s *Store) ReversePayment(ctx context.Context, paymentID, reference, createdBy string) (_ CreditReversal, err error) {
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
		grant := CreditGrant{
			ID: id, Team: paid.Team, Kind: CreditGrantReversal, AmountMicro: already - paid.AmountMicro,
			Reference: reference, Reverses: paymentID, CreatedBy: createdBy, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `
        INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses, created_by, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			string(grant.Team), grant.ID, grant.Kind, grant.AmountMicro, grant.Reference, grant.Reverses,
			grant.CreatedBy, now.UnixNano()); err != nil {
			return CreditReversal{}, fmt.Errorf("credits: insert reversal: %w", err)
		}
		out.Grant, out.Created = &grant, true
		already = paid.AmountMicro
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

// HoldTeamForDispute holds team's cloud usage for disputeID. A held team's
// metered claims are refused, so no new cloud work starts, while work already
// running finishes; the team stays held while any of its holds is
// unreleased. Holding a dispute that already has a hold, released or not,
// writes nothing, so a replayed event never undoes an operator's release. It
// reports whether it wrote the hold, and returns [ErrUnknownTeam] for a team
// that is not registered.
func (s *Store) HoldTeamForDispute(ctx context.Context, team Team, disputeID, reason string, now time.Time) (bool, error) {
	team = NormalizeTeam(team)
	disputeID = strings.TrimSpace(disputeID)
	if disputeID == "" {
		return false, errors.New("credits: a hold names the dispute it is for")
	}
	var registered int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM teams WHERE name = ?`, string(team)).Scan(&registered); err != nil {
		return false, err
	}
	if registered == 0 {
		return false, fmt.Errorf("%w: %s", ErrUnknownTeam, team)
	}
	res, err := s.exec(ctx, `
		INSERT INTO credit_freezes (team, dispute_id, reason, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (team, dispute_id) DO NOTHING`,
		string(team), disputeID, truncate(reason, 500), now.UnixNano())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseCreditFreezes releases holds on team and reports how many it
// released: the one hold of disputeID, or every hold when disputeID is empty.
// [Store.DisputeTeam] finds the team of a dispute named alone.
func (s *Store) ReleaseCreditFreezes(ctx context.Context, team Team, disputeID string, now time.Time) (int64, error) {
	team = NormalizeTeam(team)
	if team == "" {
		return 0, errors.New("credits: a release names the team")
	}
	query := `UPDATE credit_freezes SET released_at = ? WHERE team = ? AND released_at IS NULL`
	args := []any{now.UnixNano(), string(team)}
	if disputeID = strings.TrimSpace(disputeID); disputeID != "" {
		query, args = query+` AND dispute_id = ?`, append(args, disputeID)
	}
	res, err := s.exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
