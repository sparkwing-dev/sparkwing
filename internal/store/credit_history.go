package store

import (
	"context"
	"time"
)

// ListCreditGrants returns this tenant's newest grants, bounded by CreditHistoryMaxLimit.
func (t *Tenant) ListCreditGrants(ctx context.Context, limit int) (_ []CreditGrant, err error) {
	rows, err := t.s.query(ctx, `SELECT id, kind, amount_micro, reference, reverses, created_by, created_at
	  FROM credit_grants WHERE team = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		string(t.team), creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CreditGrant
	for rows.Next() {
		g := CreditGrant{Team: t.team}
		var created int64
		if err := rows.Scan(&g.ID, &g.Kind, &g.AmountMicro, &g.Reference, &g.Reverses,
			&g.CreatedBy, &created); err != nil {
			return nil, err
		}
		g.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListCreditCharges returns this tenant's newest charges, bounded by CreditHistoryMaxLimit.
func (t *Tenant) ListCreditCharges(ctx context.Context, limit int) (_ []CreditCharge, err error) {
	rows, err := t.s.query(ctx, `SELECT id, run_id, node_id, capability_prefix, principal, kind,
	         seconds, amount_micro, storage_bytes, cpu_class, rate_micro_per_second, charged_at
	  FROM credit_charges WHERE team = ? ORDER BY charged_at DESC, id DESC LIMIT ?`,
		string(t.team), creditLimit(limit))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []CreditCharge
	for rows.Next() {
		c := CreditCharge{Team: t.team}
		var charged int64
		if err := rows.Scan(&c.ID, &c.RunID, &c.NodeID, &c.TokenPrefix, &c.Principal, &c.Kind,
			&c.Seconds, &c.AmountMicro, &c.StorageBytes,
			&c.CPUClassCores, &c.RateMicroPerSecond, &charged); err != nil {
			return nil, err
		}
		c.ChargedAt = time.Unix(0, charged).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}
