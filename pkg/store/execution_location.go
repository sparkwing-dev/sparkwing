package store

import "context"

// NodeClaimOrigin returns the credential and holder facts used to describe a node's execution site.
func (s *Store) NodeClaimOrigin(ctx context.Context, runID, nodeID string) (principal, holder, runner string, metered bool, err error) {
	err = s.queryRow(ctx, `SELECT n.claim_principal, COALESCE(n.claimed_by, ''), n.claim_worker_id,
       COALESCE(t.metered, 0)
  FROM nodes n LEFT JOIN tokens t ON t.prefix = n.claim_token_prefix AND t.team = n.team
 WHERE n.run_id = ? AND n.node_id = ? AND n.team = (SELECT team FROM runs WHERE id = ?)`, runID, nodeID, runID).Scan(&principal, &holder, &runner, &metered)
	return principal, holder, runner, metered, err
}

// TriggerClaimOrigin returns the current trigger claim when generation still matches an attempt.
func (s *Store) TriggerClaimOrigin(ctx context.Context, runID string, generation int64) (principal string, metered bool, err error) {
	err = s.queryRow(ctx, `SELECT t.claim_principal, COALESCE(k.metered, 0)
  FROM triggers t LEFT JOIN tokens k ON k.prefix = t.claim_token_prefix AND k.team = t.team
 WHERE t.id = ? AND t.claim_seq = ? AND t.team = (SELECT team FROM runs WHERE id = ?)`,
		runID, generation, runID).Scan(&principal, &metered)
	return principal, metered, err
}
