package store

import (
	"context"
	"time"
)

// RunCreditUsage is what one run cost a team in runner time: its nodes and its trigger step, reservations, usage and refunds netted together.
type RunCreditUsage struct {
	RunID         string
	Seconds       int64
	AmountMicro   int64
	LastChargedAt time.Time
}

// CreditUsageByRun returns the team's runner spend grouped by run, the most recently charged run first, at most limit runs.
func (t *Tenant) CreditUsageByRun(ctx context.Context, limit int) (_ []RunCreditUsage, err error) {
	mode, err := creditPaymentMode(ctx, storeRowQuerier{t.s})
	if err != nil {
		return nil, err
	}
	rows, err := t.s.query(ctx, `SELECT run_id, SUM(seconds), SUM(amount_micro), MAX(charged_at)
	  FROM credit_charges WHERE team = ? AND payment_mode = ? AND kind != ? AND run_id != ''
	 GROUP BY run_id ORDER BY MAX(charged_at) DESC, run_id DESC LIMIT ?`,
		string(t.team), mode, CreditChargeStorage, creditLimit(limit))
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
