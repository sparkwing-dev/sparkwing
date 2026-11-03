package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// GetLatestProvenRunAtCommit returns the newest run of pipeline at the reading
// run's own sealed commit whose repository was proven, by GitHub or by the
// controller's own fetch, as the reader's proven repository, and whose sealed
// options selected every job and step without dry run, that sealed no
// environment variables, and whose job succeeded. Returns ErrNotFound when none
// qualifies.
func (t *Tenant) GetLatestProvenRunAtCommit(ctx context.Context, pipeline, job, readerRunID string, statuses []string, maxAge time.Duration) (*Run, error) {
	if t.team == "" {
		return nil, ErrNoTeam
	}
	reader, err := t.ReadRun(ctx, readerRunID)
	if err != nil {
		return nil, err
	}
	query, args, ok := provenRunAtCommitQuery(t.s.dialect, t.team, pipeline, reader.Request)
	if !ok {
		return nil, notFound("pipeline run", pipeline)
	}
	// safety: a newer run that skipped the job holds no output, and must not hide an older run that has one.
	query += ` AND EXISTS (SELECT 1 FROM nodes WHERE nodes.team=? AND nodes.run_id=runs.id AND nodes.node_id=? AND nodes.status='done' AND nodes.outcome IN ('success','memoized'))`
	return t.s.getLatestRun(ctx, query, append(args, t.team, job), pipeline, statuses, maxAge, true)
}

// safety: the output read after a proven lookup re-proves the run, so a run id
// the reader learned elsewhere cannot borrow the proven reference.
func provenAtCommitForReaderTx(ctx context.Context, tx *storeTx, team Team, readerRunID string, candidate RunRow) (bool, error) {
	reader, err := readRunTx(ctx, tx, team, readerRunID)
	if err != nil {
		return false, err
	}
	query, args, ok := provenRunAtCommitQuery(tx.dialect, team, candidate.Pipeline, reader.Request)
	if !ok {
		return false, nil
	}
	var matched Team
	var id string
	err = tx.QueryRowContext(ctx, query+" AND id=?", append(args, candidate.ID)...).Scan(&matched, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// safety: the commit and repository come from the reader's sealed request, never the caller, and an asserted run
// proves neither. A run narrowed by only, start-at or stop-at, or run dry, skipped work its output does not admit
// to, and a sealed environment such as GOFLAGS can reshape what its jobs judged, so neither counts.
func provenRunAtCommitQuery(dialect Dialect, team Team, pipeline string, reader runspec.SealedRequest) (string, []any, bool) {
	repository, sha := reader.Provenance.ProvenRepository(), reader.Provenance.ProvenSHA()
	if repository == "" || sha == "" {
		return "", nil, false
	}
	q := claimSQL{dialect: dialect}
	request := q.blob("request_json")
	provenance := q.at(request, "provenance")
	clean := make([]string, 0, 5)
	for _, option := range []string{"start_at", "stop_at", "only"} {
		clean = append(clean, "COALESCE("+q.at(request, "request", "options", option, "value")+",'')=''")
	}
	clean = append(clean, "COALESCE(CAST("+q.at(request, "request", "options", "dry_run", "value")+" AS TEXT),'false') NOT IN ('true','1')")
	clean = append(clean, "NOT EXISTS (SELECT 1 FROM "+q.array(q.at(request, "request", "env", "names"), "env_name")+")")
	return `SELECT team,id FROM runs WHERE team=? AND repo_key=? AND provenance_sha=? AND pipeline=? AND intake_mode='execute' AND ` +
			q.provenRepository(provenance) + `=? AND ((` + trustIn(q.at(provenance, "trust"), runspec.GitHubCommitTrust) + ` AND ` +
			q.at(provenance, "sha") + `=?) OR ` + q.at(provenance, "fetched_sha") + `=?) AND ` + strings.Join(clean, " AND "),
		[]any{team, string(repository), sha, pipeline, string(repository), sha, sha}, true
}

func (s *Store) getLatestRun(ctx context.Context, query string, args []any, pipeline string, statuses []string, maxAge time.Duration, fromAnyRun bool) (*Run, error) {
	if pipeline == "" {
		return nil, errors.New("GetLatestRun: pipeline is required")
	}
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackOrLog(tx)
	q := claimSQL{dialect: s.dialect}
	if !fromAnyRun {
		query += " AND intake_mode<>'publish' AND " + q.defaultBranchWork(q.at(q.blob("request_json"), "provenance"))
	}
	if len(statuses) > 0 {
		placeholders := make([]string, len(statuses))
		for i, status := range statuses {
			placeholders[i] = "?"
			args = append(args, status)
		}
		query += " AND status IN (" + strings.Join(placeholders, ",") + ")"
	}
	if maxAge > 0 {
		query += " AND COALESCE(finished_at,started_at,created_at)>=?"
		args = append(args, tx.now(ctx).Add(-maxAge).UnixNano())
	}
	var team Team
	var id string
	err = tx.QueryRowContext(ctx, query+" ORDER BY COALESCE(started_at,created_at) DESC,number DESC,id DESC LIMIT 1", args...).Scan(&team, &id)
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
	display := row.Display()
	args := display.RequestedArgs
	if row.Spec != nil {
		args = display.Args
	}
	run := &Run{PlanSnapshot: row.Plan, RunSummary: runstate.RunSummary{ID: row.ID, Pipeline: row.Pipeline, Status: string(row.State), CreatedAt: row.CreatedAt, StartedAt: row.CreatedAt, FinishedAt: row.FinishedAt, TriggerSource: string(row.Request.Request.Origin), GitBranch: display.Git.Ref, GitSHA: display.Git.SHA, DeclaredRepo: display.Git.Repository.Display, Args: args, ParentRunID: row.Lineage.ParentRunID, RetryOf: row.Lineage.RetryOf, RepoInherited: row.Request.Request.Lineage.RepoInherited, Invocation: map[string]any{"args": args, InvocationSecretArgsKey: display.SecretArgs, "reproducer": display.Reproducer}}}
	if row.StartedAt != nil {
		run.StartedAt = *row.StartedAt
	}
	return run
}
