package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func importPublishedAttemptTx(ctx context.Context, tx *storeTx, team Team, row *NodeRow, change NodeTransition) error {
	a := change.Extra.Publication
	if a == nil || a.Attempt <= 0 || a.Generation <= 0 || a.StartedAt.IsZero() || a.FinishedAt.Before(a.StartedAt) || a.Executor == "" ||
		!slices.Contains([]NodeState{NodeSuccess, NodeFailed, NodeCancelled, NodeSkipped, NodeSuperseded, NodeMemoized}, NodeState(a.Outcome)) || change.Extra.Result != nil || row.Attempt != 0 || row.Generation != 0 {
		return authorityError(apierr.BadRequest, "publication requires one completed local attempt")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at,finished_at,outcome) VALUES (?,?,?,?,?,?,?,'local',?,'local',?,?,?)`, team, row.Ref.RunID, row.Ref.NodeID, a.Attempt, a.Generation, change.Actor.Authority.ID, change.Actor.CapabilityPrefix, a.Executor, a.StartedAt.UnixNano(), a.FinishedAt.UnixNano(), a.Outcome)
	if err != nil {
		return err
	}
	row.Attempt, row.Generation, row.StartedAt = a.Attempt, a.Generation, &a.StartedAt
	return nil
}
