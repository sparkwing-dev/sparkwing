package store

import (
	"context"
	"database/sql"
	"time"
)

// LegacyAgentClaim is one recent claim made outside enrolled-executor mode.
type LegacyAgentClaim struct {
	RunID     string
	Status    string
	ClaimedBy string
	LastSeen  time.Time
}

// ListLegacyAgentClaims returns claims whose lease falls inside the observation
// window. Enrolled executor claims are reported through the executor registry.
func (s *Store) ListLegacyAgentClaims(ctx context.Context, since time.Time) (_ []LegacyAgentClaim, err error) {
	rows, err := s.query(ctx, `
SELECT run_id, status, claimed_by, COALESCE(started_at, 0), COALESCE(lease_expires_at, 0)
  FROM nodes
 WHERE claimed_by IS NOT NULL AND claimed_by != ''
   AND claim_executor = ''
   AND lease_expires_at IS NOT NULL AND lease_expires_at >= ?
`, since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []LegacyAgentClaim
	for rows.Next() {
		var claim LegacyAgentClaim
		var started, expires int64
		if err := rows.Scan(&claim.RunID, &claim.Status, &claim.ClaimedBy, &started, &expires); err != nil {
			return nil, err
		}
		claim.LastSeen = time.Unix(0, max(started, expires))
		out = append(out, claim)
	}
	return out, rows.Err()
}

// RunTrend is the store-owned input to the controller's time buckets.
type RunTrend struct {
	RunID      string
	Pipeline   string
	Status     string
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt *time.Time
	Cached     bool
}

// ListRunTrends returns every run in the requested window and whether all of
// its nodes completed from cached or already-satisfied work.
func (t *Tenant) ListRunTrends(ctx context.Context, since time.Time, pipeline string) (_ []RunTrend, err error) {
	query := `
SELECT id, pipeline, status, created_at, started_at, finished_at
     , CASE
         WHEN EXISTS (SELECT 1 FROM nodes WHERE nodes.run_id = runs.id)
          AND NOT EXISTS (
                SELECT 1
                  FROM nodes
                 WHERE nodes.run_id = runs.id
                   AND COALESCE(nodes.outcome, '') NOT IN ('cached', 'satisfied')
              )
         THEN 1 ELSE 0
       END AS all_cached
  FROM runs
 WHERE team = ? AND started_at >= ?`
	args := []any{string(t.team), since.UnixNano()}
	if pipeline != "" {
		query += " AND pipeline = ?"
		args = append(args, pipeline)
	}
	query += " ORDER BY started_at ASC"
	rows, err := t.s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []RunTrend
	for rows.Next() {
		var trend RunTrend
		var created, started int64
		var finished sql.NullInt64
		var cached int64
		if err := rows.Scan(&trend.RunID, &trend.Pipeline, &trend.Status, &created, &started, &finished, &cached); err != nil {
			return nil, err
		}
		trend.CreatedAt = time.Unix(0, created)
		trend.StartedAt = time.Unix(0, started)
		trend.Cached = cached != 0
		if finished.Valid {
			at := time.Unix(0, finished.Int64)
			trend.FinishedAt = &at
		}
		out = append(out, trend)
	}
	return out, rows.Err()
}
