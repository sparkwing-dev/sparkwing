package teststore

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SeedRunFinish sets terminal state for fixtures that do not exercise run execution.
func SeedRunFinish(s *store.Store, ctx context.Context, team store.Team, id, status, message string) error {
	query := `UPDATE runs SET status=?,error=?,finished_at=? WHERE team=? AND id=?`
	if s.Dialect() == store.DialectPostgres {
		query = `UPDATE runs SET status=$1,error=$2,finished_at=$3 WHERE team=$4 AND id=$5`
	}
	result, err := s.DB().ExecContext(ctx, query, status, message, time.Now().UnixNano(), team, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("fixture run %s/%s: matched %d rows", team, id, n)
	}
	return nil
}

// SeedNodeFinish sets terminal facts for fixtures that do not exercise job execution.
func SeedNodeFinish(s *store.Store, ctx context.Context, team store.Team, ref store.NodeRef, result store.NodeResult) error {
	query := `UPDATE nodes SET status='done',outcome=?,error=?,failure_reason=?,exit_code=?,finished_at=?,
claimed_by=NULL,lease_expires_at=NULL,capability_prefix='',owner_capability_prefix=''
WHERE team=? AND run_id=? AND node_id=?`
	if s.Dialect() == store.DialectPostgres {
		query = `UPDATE nodes SET status='done',outcome=$1,error=$2,failure_reason=$3,exit_code=$4,finished_at=$5,
claimed_by=NULL,lease_expires_at=NULL,capability_prefix='',owner_capability_prefix=''
WHERE team=$6 AND run_id=$7 AND node_id=$8`
	}
	write, err := s.DB().ExecContext(ctx, query, result.Outcome, result.MaskedError, result.Reason, result.ExitCode, time.Now().UnixNano(), team, ref.RunID, ref.NodeID)
	if err != nil {
		return err
	}
	n, err := write.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("fixture job %s/%s/%s: matched %d rows", team, ref.RunID, ref.NodeID, n)
	}
	return nil
}
