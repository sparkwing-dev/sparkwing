package store

import (
	"context"
	"errors"
	"fmt"
)

// safety: lockLedgerTx is the only constructor of a ledgerTx, and only its methods write credit_grants or
// credit_charges, so every ledger write runs under its team's lock.
type ledgerTx struct {
	*storeTx
	team Team
}

// safety: PostgreSQL runs concurrent claims and settlements in their own transactions, so a team's balance check and
// the write it guards serialize on the team's lock; SQLite allows one writer and is already serial. Teams never share
// a lock, so one team's claims never wait on another's.
func lockLedgerTx(ctx context.Context, tx *storeTx, team Team) (*ledgerTx, error) {
	if team == "" {
		return nil, errors.New("ledger: a ledger lock names its team")
	}
	if tx.dialect == DialectPostgres {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext(?))`, "sparkwing/ledger/"+string(team)); err != nil {
			return nil, err
		}
	}
	return &ledgerTx{storeTx: tx, team: team}, nil
}

func (l *ledgerTx) insertGrant(ctx context.Context, g CreditGrant) error {
	if g.Team != l.team {
		return fmt.Errorf("ledger: grant for %q under %q's lock", g.Team, l.team)
	}
	_, err := l.ExecContext(ctx, `INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses, created_by,
	    created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(g.Team), g.ID, g.Kind, g.AmountMicro, g.Reference, g.Reverses, g.CreatedBy, g.CreatedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("credits: insert %s grant: %w", g.Kind, err)
	}
	return nil
}

func (l *ledgerTx) insertCharge(ctx context.Context, c CreditCharge) error {
	if c.Team != l.team {
		return fmt.Errorf("ledger: charge for %q under %q's lock", c.Team, l.team)
	}
	_, err := l.ExecContext(ctx, `INSERT INTO credit_charges (team, id, run_id, node_id, capability_prefix, principal,
	    kind, seconds, amount_micro, storage_bytes, cpu_class, rate_micro_per_second, charged_at)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(c.Team), c.ID, c.RunID, c.NodeID, c.TokenPrefix, c.Principal, c.Kind, c.Seconds, c.AmountMicro,
		c.StorageBytes, c.CPUClassCores, c.RateMicroPerSecond, c.ChargedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("credits: insert %s charge: %w", c.Kind, err)
	}
	return nil
}

func (l *ledgerTx) balance(ctx context.Context) (int64, error) {
	return creditBalanceTx(ctx, l.storeTx, l.team)
}
