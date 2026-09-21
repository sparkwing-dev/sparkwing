package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CreateRun writes the run into t's team.
func (t *Tenant) CreateRun(ctx context.Context, r Run) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := t.s.createRunTx(ctx, tx, t.team, r); err != nil {
		return err
	}
	return tx.Commit()
}

// GetRun fetches one of t's runs by id. A run of another team reads as
// [ErrNotFound], because a caller told the row exists but is not yours
// learns that the id is in use elsewhere.
func (t *Tenant) GetRun(ctx context.Context, runID string) (*Run, error) {
	row := t.s.queryRow(ctx, `
SELECT `+runColumns+`
  FROM runs WHERE team = ? AND id = ?`, string(t.team), runID)
	run, err := scanRun(row)
	if errors.Is(err, ErrNotFound) {
		return nil, notFound("run", runID)
	}
	if err != nil {
		return nil, err
	}
	if err := t.s.loadAgentLossRetry(ctx, t.team, run); err != nil {
		return nil, err
	}
	return run, nil
}

// ListRuns returns t's runs, newest first, filtered by f.
func (t *Tenant) ListRuns(ctx context.Context, f RunFilter) ([]*Run, error) {
	return t.s.listRuns(ctx, oneTeam(t.team), f)
}

// CountRuns returns how many of t's runs match f, ignoring its Limit.
func (t *Tenant) CountRuns(ctx context.Context, f RunFilter) (int, error) {
	return t.s.countRuns(ctx, oneTeam(t.team), f)
}

// FinishRun marks one of t's runs terminal with the given status and
// optional error. A run of another team reads as [ErrNotFound], the same
// as [Tenant.GetRun] reports it, because a mutator that quietly matched
// nothing would let a cancel endpoint answer 200 and cancel nothing.
func (t *Tenant) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := assertRunBelongsToTeamTx(ctx, tx, t.team, runID); err != nil {
		return err
	}
	if err := t.s.assertRunMutationFenceTx(ctx, tx, t.team, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, finishRunStmt+` AND team = ?`,
		status, errMsg, time.Now().UnixNano(), runID, string(t.team)); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishRunsIfActive atomically finalizes the named non-terminal runs
// of t's team. A failure rolls back every member, so one shared lease
// cannot be partly cancelled, and a member of another team fails the
// whole batch with [ErrNotFound] rather than being skipped.
func (t *Tenant) FinishRunsIfActive(ctx context.Context, runIDs []string, status, errMsg string) error {
	if len(runIDs) == 0 {
		return nil
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	now := time.Now().UnixNano()
	for _, runID := range runIDs {
		if err := assertRunBelongsToTeamTx(ctx, tx, t.team, runID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET status = ?, error = ?, finished_at = ?
			  WHERE team = ? AND id = ? AND status NOT IN ('success','failed','cancelled')`,
			status, errMsg, now, string(t.team), runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TouchRunHeartbeat stamps last_heartbeat_at=now on one of t's runs. A
// run of another team reads as [ErrNotFound].
func (t *Tenant) TouchRunHeartbeat(ctx context.Context, runID string) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	if err := assertRunBelongsToTeamTx(ctx, tx, t.team, runID); err != nil {
		return err
	}
	if err := t.s.assertRunHeartbeatFenceTx(ctx, tx, t.team, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET last_heartbeat_at = ? WHERE team = ? AND id = ?`,
		time.Now().UnixNano(), string(t.team), runID); err != nil {
		return err
	}
	return tx.Commit()
}

// safety: every tenant mutator runs this first, because a scoped UPDATE
// matching nothing cannot be told from one that matched and changed
// nothing, and a caller told "no error" for another team's id reads and
// writes two different worlds.
func assertRunBelongsToTeamTx(ctx context.Context, tx *storeTx, team Team, runID string) error {
	var found int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM runs WHERE team = ? AND id = ?`, string(team), runID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("run", runID)
	}
	return err
}
