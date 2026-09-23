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

// TeamCreditFreeze is whether a team's cloud usage is held, and why.
type TeamCreditFreeze struct {
	Frozen   bool
	FrozenAt time.Time
	Reason   string
}

// SetTeamCreditFreeze holds or releases a team's cloud usage. A frozen team's
// metered claims are refused, so no new cloud work starts, until the operator
// or the dispute's outcome releases it; work already running finishes. It
// returns [ErrUnknownTeam] for a team that is not registered.
func (s *Store) SetTeamCreditFreeze(ctx context.Context, team Team, frozen bool, reason string, now time.Time) error {
	team = NormalizeTeam(team)
	at := int64(0)
	if frozen {
		at = now.UnixNano()
	} else {
		reason = ""
	}
	res, err := s.exec(ctx,
		`UPDATE teams SET credit_frozen_at = ?, credit_frozen_reason = ? WHERE name = ?`,
		at, truncate(reason, 500), string(team))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrUnknownTeam, team)
	}
	return nil
}

// CreditFreeze reports whether t's cloud usage is held.
func (t *Tenant) CreditFreeze(ctx context.Context) (TeamCreditFreeze, error) {
	var at int64
	var reason string
	err := t.s.queryRow(ctx,
		`SELECT credit_frozen_at, credit_frozen_reason FROM teams WHERE name = ?`, string(t.team)).Scan(&at, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamCreditFreeze{}, nil
	}
	if err != nil {
		return TeamCreditFreeze{}, err
	}
	return creditFreeze(at, reason), nil
}

func teamCreditFreezeTx(ctx context.Context, tx *storeTx, team Team) (TeamCreditFreeze, error) {
	var at int64
	var reason string
	err := tx.QueryRowContext(ctx,
		`SELECT credit_frozen_at, credit_frozen_reason FROM teams WHERE name = ?`, string(team)).Scan(&at, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamCreditFreeze{}, nil
	}
	if err != nil {
		return TeamCreditFreeze{}, err
	}
	return creditFreeze(at, reason), nil
}

func creditFreeze(at int64, reason string) TeamCreditFreeze {
	if at == 0 {
		return TeamCreditFreeze{}
	}
	return TeamCreditFreeze{Frozen: true, FrozenAt: time.Unix(0, at).UTC(), Reason: reason}
}
