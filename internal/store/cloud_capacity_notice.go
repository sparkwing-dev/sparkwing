package store

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// The bootstrap claim is waiting for dedicated capacity until activation seals
// its execution host. Using the claim time keeps elapsed time stable across polls.
func readCloudCapacityWaitTx(ctx context.Context, tx *storeTx, team Team, run RunRow) (*time.Time, error) {
	if run.FinishedAt != nil || run.CancelRequestedAt != nil {
		return nil, nil
	}
	now := tx.now(ctx).UnixNano()
	rows, err := tx.queryContext(ctx, `SELECT c.prefix,c.created_at FROM capabilities c JOIN (
SELECT capability_prefix AS prefix,lease_expires_at AS lease FROM triggers WHERE team=? AND id=? AND status='claimed'
UNION ALL SELECT capability_prefix,lease_expires_at FROM nodes WHERE team=? AND run_id=? AND claimed_by IS NOT NULL AND status<>'done'
UNION ALL SELECT capability_prefix,lease_expires_at FROM eval_units WHERE team=? AND run_id=? AND status IN ('claimed','running')
) u ON u.prefix=c.prefix WHERE c.team=? AND c.executor_class='cloud' AND c.delivery_purpose='bootstrap' AND u.lease>? AND `+liveCredentialSQL("c.")+` ORDER BY c.created_at`, team, run.ID, team, run.ID, team, run.ID, team, now, now, now)
	if err != nil {
		return nil, err
	}
	type pending struct {
		prefix string
		since  int64
	}
	var candidates []pending
	for rows.Next() {
		var candidate pending
		if err := rows.Scan(&candidate.prefix, &candidate.since); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		candidates = append(candidates, candidate)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		c, err := loadCapabilityTx(ctx, tx, candidate.prefix)
		if err != nil {
			return nil, err
		}
		if c.ExecutionHost == "" && c.Delivery == capability.DeliveryBootstrap {
			since := time.Unix(0, candidate.since).UTC()
			return &since, nil
		}
	}
	return nil, nil
}
