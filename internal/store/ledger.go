package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ledgerDollarQuote = regexp.MustCompile(`(?i)^\$(?:[a-z_][a-z_0-9]*)?\$`)

var ledgerWrite = regexp.MustCompile(`(?is)\b(insert(?:\s+or\s+\w+)?\s+into|replace\s+into|update(?:\s+or\s+\w+)?|delete\s+from|merge\s+into)\s+(?:only\s*)?\(?\s*(?:[a-z_][a-z_0-9$]*\s*\.\s*)?(credit_grants|credit_charges)\b`)

// TRUNCATE can reach the ledger through another target's CASCADE.
var ledgerTruncate = regexp.MustCompile(`(?i)(?:^|;)\s*truncate\b`)

type refusedLedgerRow struct{ err error }

func (r refusedLedgerRow) Scan(...any) error { return r.err }

// safety: inspect assembled SQL before execution so a ledger mutation cannot skip its team's lock.
func refuseLedgerWrite(q string) error {
	lower := strings.ToLower(q)
	if !strings.Contains(lower, "credit_") && !strings.Contains(lower, "truncate") {
		return nil
	}
	statement := ledgerSQL(q)
	if ledgerWrite.MatchString(statement) || ledgerTruncate.MatchString(statement) {
		return errors.New("ledger: credit_grants and credit_charges are written only through ledgerTx")
	}
	return nil
}

// SQL is inspected after assembly; comments and values cannot hide or impersonate its write target.
func ledgerSQL(q string) string {
	var out strings.Builder
	for i := 0; i < len(q); {
		switch {
		case strings.HasPrefix(q[i:], "--"):
			end := strings.IndexAny(q[i:], "\r\n")
			if end < 0 {
				return out.String()
			}
			i += end
			out.WriteByte(' ')
		case strings.HasPrefix(q[i:], "/*"):
			i += 2
			for depth := 1; i < len(q) && depth > 0; {
				switch {
				case strings.HasPrefix(q[i:], "/*"):
					depth++
					i += 2
				case strings.HasPrefix(q[i:], "*/"):
					depth--
					i += 2
				default:
					i++
				}
			}
			out.WriteByte(' ')
		case q[i] == '$':
			tag := ledgerDollarQuote.FindString(q[i:])
			if tag == "" {
				out.WriteByte(q[i])
				i++
				continue
			}
			i += len(tag)
			end := strings.Index(q[i:], tag)
			if end < 0 {
				return out.String()
			}
			i += end + len(tag)
			out.WriteByte('?')
		case q[i] == '\'' || q[i] == '"' || q[i] == '`' || q[i] == '[':
			quote := q[i]
			out.WriteByte(' ')
			escaped := quote == '\'' && i > 0 && (q[i-1] == 'e' || q[i-1] == 'E')
			end := quote
			if quote == '[' {
				end = ']'
			}
			i++
			for i < len(q) {
				if escaped && q[i] == '\\' && i+1 < len(q) {
					i += 2
					continue
				}
				if q[i] == end {
					i++
					if i >= len(q) || q[i] != end {
						break
					}
				}
				if quote != '\'' {
					out.WriteByte(q[i])
				}
				i++
			}
			if quote == '\'' {
				out.WriteByte('?')
			}
			out.WriteByte(' ')
		default:
			out.WriteByte(q[i])
			i++
		}
	}
	return out.String()
}

// safety: this is the one statement path allowed to write the ledger tables, and only ledgerTx holds it.
func (l *ledgerTx) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return l.tx.ExecContext(ctx, rewritePh(l.dialect, q), args...)
}

// safety: lockLedgerTx is the only constructor of a ledgerTx, and only its methods write credit_grants or
// credit_charges, so every ledger write runs under its team's lock.
type ledgerTx struct {
	*storeTx
	team Team
}

type ledgerView struct {
	tx   *storeTx
	team Team
}

func (l *ledgerTx) view() ledgerView { return ledgerView{tx: l.storeTx, team: l.team} }

func (v ledgerView) balance(ctx context.Context) (int64, error) {
	return creditBalanceTx(ctx, v.tx, v.team)
}

// safety: PostgreSQL runs concurrent claims and settlements in their own transactions, so a team's balance check and
// the write it guards serialize on the team's lock; SQLite allows one writer and is already serial. The lock key is
// a 32-bit hash of the team, so two teams can share one and serialize; that costs only waiting, never a balance.
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
	_, err := l.exec(ctx, `INSERT INTO credit_grants (team, id, kind, amount_micro, reference, reverses, created_by,
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
	_, err := l.exec(ctx, `INSERT INTO credit_charges (team, id, run_id, node_id, capability_prefix, principal,
	    kind, seconds, amount_micro, storage_bytes, cpu_class, rate_micro_per_second, charged_at)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(c.Team), c.ID, c.RunID, c.NodeID, c.TokenPrefix, c.Principal, c.Kind, c.Seconds, c.AmountMicro,
		c.StorageBytes, c.CPUClassCores, c.RateMicroPerSecond, c.ChargedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("credits: insert %s charge: %w", c.Kind, err)
	}
	return nil
}

// safety: usage not paid ahead, such as retained storage, draws at most the team's positive balance, so it never
// puts a team into debt; what the balance cannot pay is forgiven, not carried.
func (l *ledgerTx) debitFunded(ctx context.Context, c CreditCharge) (*CreditCharge, error) {
	if c.Team != l.team {
		return nil, fmt.Errorf("ledger: charge for %q under %q's lock", c.Team, l.team)
	}
	if cause, err := teamHoldTx(ctx, l.storeTx, l.team); err != nil || cause != "" {
		return nil, err
	}
	balance, err := l.balance(ctx)
	if err != nil {
		return nil, err
	}
	c.AmountMicro = min(c.AmountMicro, max(balance, 0))
	if c.AmountMicro <= 0 {
		return nil, nil
	}
	return &c, l.insertCharge(ctx, c)
}

func (l *ledgerTx) balance(ctx context.Context) (int64, error) {
	return l.view().balance(ctx)
}

func (l *ledgerTx) relabelCreator(ctx context.Context, from, to string) error {
	_, err := l.exec(ctx, `UPDATE credit_grants SET created_by=? WHERE team=? AND created_by=?`, to, l.team, from)
	return err
}

func (l *ledgerTx) purge(ctx context.Context) error {
	for _, q := range []string{`DELETE FROM credit_charges WHERE team=?`, `DELETE FROM credit_grants WHERE team=?`} {
		if _, err := l.exec(ctx, q, l.team); err != nil {
			return err
		}
	}
	return nil
}
