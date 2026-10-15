package store

import (
	"context"
	"errors"
	"time"
)

// ListPendingApprovals returns t's unresolved approvals, oldest first. The
// team is the run's, because an approval gates one run and belongs where
// that run does.
func (t *Tenant) ListPendingApprovals(ctx context.Context) (_ []*Approval, err error) {
	rows, err := t.s.query(ctx, `
SELECT run_id, node_id, requested_at, message, timeout_ms, on_timeout,
       approver, resolved_at, resolution, comment
  FROM approvals
 WHERE resolved_at IS NULL
   AND run_id IN (SELECT id FROM runs WHERE team = ?)
 ORDER BY requested_at ASC`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []*Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAgentClaims returns the live node claims on t's runs since the given
// instant.
func (t *Tenant) ListAgentClaims(ctx context.Context, since time.Time) (_ []AgentClaim, err error) {
	rows, err := t.s.query(ctx, `
SELECT run_id, status, claimed_by, claim_token_prefix,
       COALESCE(started_at, 0), COALESCE(lease_expires_at, 0)
  FROM nodes
 WHERE claimed_by IS NOT NULL AND claimed_by != ''
   AND lease_expires_at IS NOT NULL AND lease_expires_at >= ?
   AND run_id IN (SELECT id FROM runs WHERE team = ?)
`, since.UnixNano(), string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []AgentClaim
	for rows.Next() {
		var claim AgentClaim
		var started, expires int64
		if err := rows.Scan(&claim.RunID, &claim.Status, &claim.ClaimedBy, &claim.TokenPrefix, &started, &expires); err != nil {
			return nil, err
		}
		if started > 0 {
			claim.StartedAt = time.Unix(0, started)
			claim.LastSeen = claim.StartedAt
		}
		out = append(out, claim)
	}
	return out, rows.Err()
}

// ListConcurrencyStates returns the admission picture for every one of t's
// concurrency keys with a live holder or waiter, in lexical key order.
func (t *Tenant) ListConcurrencyStates(ctx context.Context) ([]*ConcurrencyState, error) {
	keys, err := t.liveConcurrencyKeys(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]*ConcurrencyState, 0, len(keys))
	for _, k := range keys {
		st, err := t.GetConcurrencyState(ctx, k)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		states = append(states, st)
	}
	return states, nil
}

func (t *Tenant) liveConcurrencyKeys(ctx context.Context) (_ []string, err error) {
	rows, err := t.s.query(ctx,
		`SELECT key FROM concurrency_holders WHERE team = ?
		 UNION SELECT key FROM concurrency_waiters WHERE team = ?
		 ORDER BY key`, string(t.team), string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
