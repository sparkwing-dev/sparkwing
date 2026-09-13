package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const egressUsageTableSQLite = `CREATE TABLE IF NOT EXISTS egress_usage (
    -- the principal the bytes were served to, or "anonymous"
    principal  TEXT NOT NULL,
    -- the UTC month the bytes fell in, as "2006-01"
    month      TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (principal, month)
);
CREATE INDEX IF NOT EXISTS idx_egress_usage_month ON egress_usage(month);`

var egressUsageTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").
	Replace(egressUsageTableSQLite)

// EgressUsage is one principal's download total for one UTC month. The
// controller counts bytes in memory and writes these rows on a timer, so
// a restart resumes the month instead of handing every principal a fresh
// budget.
type EgressUsage struct {
	Principal string
	// Month is the UTC month the bytes fell in, as "2006-01".
	Month     string
	Bytes     int64
	UpdatedAt time.Time
}

// RecordEgressUsage writes the given totals, keeping the larger of the
// stored and the offered count for each principal and month.
//
// The total only ever rises within a month, so a writer whose memory is
// behind the row cannot lower it; that is what keeps a controller that
// restarted mid-month, or a second one writing the same database, from
// handing back budget that was already spent.
func (s *Store) RecordEgressUsage(ctx context.Context, usages []EgressUsage) error {
	if len(usages) == 0 {
		return nil
	}
	now := time.Now().UTC().UnixNano()
	for _, u := range usages {
		principal := strings.TrimSpace(u.Principal)
		month := strings.TrimSpace(u.Month)
		if principal == "" || month == "" || u.Bytes < 0 {
			continue
		}
		_, err := s.exec(ctx, `
            INSERT INTO egress_usage (principal, month, bytes, updated_at)
            VALUES (?, ?, ?, ?)
            ON CONFLICT (principal, month) DO UPDATE SET
                bytes = CASE WHEN excluded.bytes > egress_usage.bytes
                             THEN excluded.bytes ELSE egress_usage.bytes END,
                updated_at = excluded.updated_at`,
			principal, month, u.Bytes, now)
		if err != nil {
			return fmt.Errorf("record egress usage for %s: %w", principal, err)
		}
	}
	return nil
}

// ListEgressUsage returns every principal's total for one UTC month,
// largest first. An empty month returns every month's rows.
func (s *Store) ListEgressUsage(ctx context.Context, month string) (_ []EgressUsage, err error) {
	query := `SELECT principal, month, bytes, updated_at FROM egress_usage`
	args := []any{}
	if month = strings.TrimSpace(month); month != "" {
		query += ` WHERE month = ?`
		args = append(args, month)
	}
	query += ` ORDER BY bytes DESC, principal`
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list egress usage: %w", err)
	}
	defer closeRowsInto(rows, &err)
	var out []EgressUsage
	for rows.Next() {
		var (
			u       EgressUsage
			updated int64
		)
		if err := rows.Scan(&u.Principal, &u.Month, &u.Bytes, &updated); err != nil {
			return nil, fmt.Errorf("list egress usage: %w", err)
		}
		u.UpdatedAt = time.Unix(0, updated).UTC()
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list egress usage: %w", err)
	}
	return out, nil
}

// PruneEgressUsage removes rows for months before the given UTC month
// and reports how many it deleted. A budget is monthly, so older rows
// answer nothing a caller asks.
func (s *Store) PruneEgressUsage(ctx context.Context, before string) (int, error) {
	before = strings.TrimSpace(before)
	if before == "" {
		return 0, nil
	}
	res, err := s.exec(ctx, `DELETE FROM egress_usage WHERE month < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune egress usage: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune egress usage: %w", err)
	}
	return int(n), nil
}
