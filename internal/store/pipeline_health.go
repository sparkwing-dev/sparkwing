package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// PipelineHealthRun is the latest default-branch result for one repository and pipeline.
type PipelineHealthRun struct {
	ID, Repository, Pipeline, Status, RetryOf string
	CreatedAt, StartedAt                      time.Time
	FinishedAt                                *time.Time
}

// PipelineHealth returns at most limit latest root runs, plus one lookahead row.
// Branches without recorded default-branch facts use main.
func (t *Tenant) PipelineHealth(ctx context.Context, limit int) (_ []PipelineHealthRun, err error) {
	if t.team == "" {
		return nil, ErrNoTeam
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	field := func(column string, path ...string) string {
		if t.s.dialect == DialectPostgres {
			return "(convert_from(" + column + ",'UTF8')::jsonb #>> '{" + strings.Join(path, ",") + "}')"
		}
		return "json_extract(" + column + ",'$." + strings.Join(path, ".") + "')"
	}
	branch := "COALESCE(NULLIF(" + field("spec_json", "resolved", "default_branch", "value") + ",''),NULLIF(" + field("request_json", "request", "git", "default_branch", "value") + ",''),'main')"
	rows, err := t.s.query(ctx, `WITH ranked AS (
 SELECT id,repo_key,repo_display,pipeline,status,retry_of,created_at,started_at,finished_at,
 ROW_NUMBER() OVER (PARTITION BY repo_key,pipeline ORDER BY created_at DESC,number DESC,id DESC) AS rank
 FROM runs WHERE team=? AND COALESCE(parent_run_id,'')='' AND
 (git_branch=`+branch+` OR git_branch='refs/heads/' || `+branch+`))
 SELECT id,repo_display,pipeline,status,retry_of,created_at,started_at,finished_at
 FROM ranked WHERE rank=1 ORDER BY repo_key,pipeline LIMIT ?`, t.team, limit+1)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []PipelineHealthRun{}
	for rows.Next() {
		var row PipelineHealthRun
		var created int64
		var started, finished sql.NullInt64
		if err := rows.Scan(&row.ID, &row.Repository, &row.Pipeline, &row.Status, &row.RetryOf, &created, &started, &finished); err != nil {
			return nil, err
		}
		row.CreatedAt = time.Unix(0, created).UTC()
		if started.Valid {
			row.StartedAt = time.Unix(0, started.Int64).UTC()
		}
		row.FinishedAt = stateTime(finished)
		out = append(out, row)
	}
	return out, rows.Err()
}

// PendingRunCount reports the team's root runs waiting to start.
func (t *Tenant) PendingRunCount(ctx context.Context) (int, error) {
	if t.team == "" {
		return 0, ErrNoTeam
	}
	var n int
	err := t.s.queryRow(ctx, `SELECT COUNT(*) FROM runs WHERE team=? AND COALESCE(parent_run_id,'')='' AND status IN ('pending','queued')`, t.team).Scan(&n)
	return n, err
}
