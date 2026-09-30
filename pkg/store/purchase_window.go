package store

import (
	"context"
	"database/sql"
	"time"
)

// safety: a reversal is matched to the payment it names rather than to its own
// date, so a refund settled after the window still takes back its payment's
// purchase, and a refund of a payment that has aged out changes nothing.
const recentPaidGrantsSQL = `SELECT COALESCE(SUM(g.amount_micro), 0) FROM credit_grants g
  WHERE g.team = ? AND ((g.kind = ? AND g.created_at >= ?)
     OR (g.kind = ? AND g.reverses != '' AND EXISTS (
           SELECT 1 FROM credit_grants p
            WHERE p.team = g.team AND p.kind = ? AND p.created_at >= ? AND p.reference != ''
              AND p.reference = g.reverses)))`

func recentPaidGrantsMicro(ctx context.Context, q rowQuerier, team Team, now time.Time) (int64, error) {
	var micro int64
	since := now.Add(-PurchaseLimitWindow).UnixNano()
	if err := q.QueryRowContext(ctx, recentPaidGrantsSQL,
		string(team), CreditGrantPaid, since, CreditGrantReversal, CreditGrantPaid, since).Scan(&micro); err != nil {
		return 0, err
	}
	if micro < 0 {
		return 0, nil
	}
	return micro, nil
}

type storeRowQuerier struct{ s *Store }

func (q storeRowQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.s.queryRow(ctx, query, args...)
}
