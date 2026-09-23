package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// What a team may buy and hold. A purchase is between five and five hundred
// dollars, and a balance holds at most five thousand: a per-purchase limit
// alone is defeated by buying twice, so the bound that matters is on the
// balance. It caps both the harm of a conversion mistake and the prepaid
// credit sitting unspent.
const (
	CreditPurchaseMinCents = 500
	CreditPurchaseMaxCents = 50_000

	// MaxTeamBalanceMicro is the most a team's balance may hold, five
	// thousand dollars.
	MaxTeamBalanceMicro = 5_000 * 100 * MicroCreditsPerCent
)

// ErrCreditBalanceCap is returned when a grant or a purchase would lift a
// team's balance above [MaxTeamBalanceMicro]. The refusal is a
// [CreditBalanceCapError], which wraps it and names the figures.
var ErrCreditBalanceCap = errors.New("credits: the team balance cap refuses this amount")

// CreditBalanceCapError refuses an amount that would take a team's balance
// past the cap and says what the team holds, what it asked for, and the cap.
type CreditBalanceCapError struct {
	BalanceMicro int64
	AmountMicro  int64
	CapMicro     int64
}

func (e *CreditBalanceCapError) Error() string {
	room := max(e.CapMicro-e.BalanceMicro, 0)
	return fmt.Sprintf("a team holds at most $%s of credit; this team holds $%s, so at most $%s more fits, not $%s",
		microDollars(e.CapMicro), microDollars(e.BalanceMicro), microDollars(room), microDollars(e.AmountMicro))
}

// Unwrap reports [ErrCreditBalanceCap].
func (e *CreditBalanceCapError) Unwrap() error { return ErrCreditBalanceCap }

// microDollars renders micro-credits as dollars to the cent, rounding down.
func microDollars(micro int64) string {
	cents := micro / MicroCreditsPerCent
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// safety: the check runs with the balance the grant would add to, under the
// ledger lock the grant holds, so two grants racing for the last room below
// the cap cannot both land.
func refuseAboveBalanceCap(balance, amount int64) error {
	if amount <= 0 || balance+amount <= MaxTeamBalanceMicro {
		return nil
	}
	return &CreditBalanceCapError{BalanceMicro: balance, AmountMicro: amount, CapMicro: MaxTeamBalanceMicro}
}

// CheckCreditPurchase reports whether a purchase of amountMicro fits under the
// team's balance cap now, returning a [CreditBalanceCapError] when it does
// not. A checkout calls it before any money moves; the grant that follows the
// payment checks again, because two open checkouts can both pass here.
func (t *Tenant) CheckCreditPurchase(ctx context.Context, amountMicro int64) error {
	balance, err := t.CreditBalanceMicro(ctx)
	if err != nil {
		return err
	}
	return refuseAboveBalanceCap(balance, amountMicro)
}

// RunCreditUsage is what one run cost a team in runner time: its nodes and
// its trigger step, reservations, usage and refunds netted together.
type RunCreditUsage struct {
	RunID         string
	Seconds       int64
	AmountMicro   int64
	LastChargedAt time.Time
}

// CreditUsageByRun returns the team's runner spend grouped by run, the most
// recently charged run first, at most limit runs. Storage charges bill no run
// and are left out.
func (t *Tenant) CreditUsageByRun(ctx context.Context, limit int) (_ []RunCreditUsage, err error) {
	rows, err := t.s.query(ctx, `SELECT run_id, SUM(seconds), SUM(amount_micro), MAX(charged_at)
	  FROM credit_charges WHERE team = ? AND kind != ? AND run_id != ''
	 GROUP BY run_id ORDER BY MAX(charged_at) DESC, run_id DESC LIMIT ?`,
		string(t.team), CreditChargeStorage, creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []RunCreditUsage
	for rows.Next() {
		var u RunCreditUsage
		var last int64
		if err := rows.Scan(&u.RunID, &u.Seconds, &u.AmountMicro, &last); err != nil {
			return nil, err
		}
		u.LastChargedAt = time.Unix(0, last).UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

// PaidGrantTeam returns the team whose ledger holds the paid grant carrying
// reference, which is a payment id and so unique across the deployment. A
// refund names only the payment, and this is how its reversal finds the team.
func (s *Store) PaidGrantTeam(ctx context.Context, reference string) (Team, bool, error) {
	var team string
	err := s.queryRow(ctx, `SELECT team FROM credit_grants WHERE kind = ? AND reference = ?
	  ORDER BY created_at ASC, id ASC LIMIT 1`, CreditGrantPaid, reference).Scan(&team)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return Team(team), true, nil
}
