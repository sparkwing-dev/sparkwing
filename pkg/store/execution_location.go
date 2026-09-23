package store

import "context"

// NodeClaimOrigin returns the credential and holder facts used to describe a node's execution site.
func (s *Store) NodeClaimOrigin(ctx context.Context, runID, nodeID string) (principal, holder, runner string, metered bool, err error) {
	err = s.queryRow(ctx, `SELECT n.claim_principal, COALESCE(n.claimed_by, ''), n.claim_worker_id,
       COALESCE(t.metered, 0)
  FROM nodes n LEFT JOIN tokens t ON t.prefix = n.claim_token_prefix AND t.team = n.team
 WHERE n.run_id = ? AND n.node_id = ? AND n.team = (SELECT team FROM runs WHERE id = ?)`, runID, nodeID, runID).Scan(&principal, &holder, &runner, &metered)
	return
}
