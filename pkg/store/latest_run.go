package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (s *Store) getLatestRun(ctx context.Context, scope teamScope, pipeline string, statuses []string, maxAge time.Duration) (*Run, error) {
	if pipeline == "" {
		return nil, errors.New("GetLatestRun: pipeline is required")
	}
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	where, args := "WHERE pipeline=?", []any{pipeline}
	if !scope.all {
		if scope.team == "" {
			return nil, ErrNoTeam
		}
		where += " AND team=?"
		args = append(args, string(scope.team))
	}
	if len(statuses) > 0 {
		placeholders := make([]string, len(statuses))
		for i, status := range statuses {
			placeholders[i] = "?"
			args = append(args, status)
		}
		where += " AND status IN (" + strings.Join(placeholders, ",") + ")"
	}
	if maxAge > 0 {
		where += " AND COALESCE(finished_at,started_at,created_at)>=?"
		args = append(args, tx.now().Add(-maxAge).UnixNano())
	}
	var team Team
	var id string
	err = tx.QueryRowContext(ctx, "SELECT team,id FROM runs "+where+" ORDER BY COALESCE(started_at,created_at) DESC,number DESC,id DESC LIMIT 1", args...).Scan(&team, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("pipeline run", pipeline)
	}
	if err != nil {
		return nil, err
	}
	row, err := readRunTx(ctx, tx, team, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return latestRunPresentation(row), nil
}

func latestRunPresentation(row RunRow) *Run {
	display := runspec.DisplayRequest(row.Request)
	args := display.RequestedArgs
	if row.Spec != nil {
		display = runspec.DisplaySpec(*row.Spec)
		args = display.Args
	}
	run := &Run{ID: row.ID, Pipeline: row.Pipeline, Status: string(row.State), CreatedAt: row.CreatedAt,
		StartedAt: row.CreatedAt, FinishedAt: row.FinishedAt, TriggerSource: string(row.Request.Request.Origin),
		GitBranch: display.Git.Ref, GitSHA: display.Git.SHA, DeclaredRepo: display.Git.Repository.Display,
		Args: args, PlanSnapshot: row.Plan, ParentRunID: row.Lineage.ParentRunID, RetryOf: row.Lineage.RetryOf,
		Invocation: map[string]any{"args": args, InvocationSecretArgsKey: display.SecretArgs, "reproducer": display.Reproducer},
	}
	if row.StartedAt != nil {
		run.StartedAt = *row.StartedAt
	}
	return run
}
