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

// GetLatestRun returns t's newest run for pipeline matching statuses within
// maxAge. Runs need verified default-branch provenance unless fromAnyRun is true.
// When readerRunID names a run of the same pipeline, only runs of the reader's
// repository count. Optional repositories further restrict exact repository displays.
// Returns ErrNotFound on miss.
func (t *Tenant) GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration, fromAnyRun bool, readerRunID string, repositories ...string) (*Run, error) {
	if t.team == "" {
		return nil, ErrNoTeam
	}
	// safety: repositories in one team can share a pipeline name, so a pipeline's
	// own last run, such as the staging pass a promotion relies on, must be this repository's.
	query := `SELECT team,id FROM runs WHERE team=? AND pipeline=? AND NOT EXISTS (SELECT 1 FROM runs reader WHERE reader.team=runs.team AND reader.id=? AND reader.pipeline=runs.pipeline AND reader.repo_key<>runs.repo_key)`
	args := []any{t.team, pipeline, readerRunID}
	query, args = restrictLatestRepositories(query, args, repositories)
	return t.s.getLatestRun(ctx, query, args, pipeline, statuses, maxAge, fromAnyRun)
}

// GetLatestProvenRunAtCommit returns the newest run of pipeline at the reading
// run's own sealed commit whose repository was proven, by GitHub or by the
// controller's own fetch, as the reader's proven repository, that intake sealed
// as proof eligible, whose every job that did not skip was claimed on a trusted
// host, and whose job succeeded. Returns ErrNotFound when none qualifies.
func (t *Tenant) GetLatestProvenRunAtCommit(ctx context.Context, pipeline, job, readerRunID string, statuses []string, maxAge time.Duration, repositories ...string) (*Run, error) {
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
	query, args = restrictLatestRepositories(query, append(args, t.team, job), repositories)
	return t.s.getLatestRun(ctx, query, args, pipeline, statuses, maxAge, true)
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
// proves neither. Intake's allow-list of run options decides proof eligibility, and an untrusted host may have run
// asserted work that left HOME, GOFLAGS or the build cache poisoned.
func provenRunAtCommitQuery(dialect Dialect, team Team, pipeline string, reader runspec.SealedRequest) (string, []any, bool) {
	repository, sha := reader.Provenance.ProvenRepository(), reader.Provenance.ProvenSHA()
	if repository == "" || sha == "" {
		return "", nil, false
	}
	q := claimSQL{dialect: dialect}
	request := q.blob("request_json")
	provenance := q.at(request, "provenance")
	clean := []string{
		"COALESCE(CAST(" + q.at(request, "proof_eligible") + " AS TEXT),'false') IN ('true','1')",
		"NOT EXISTS (SELECT 1 FROM nodes n WHERE n.team=? AND n.run_id=runs.id AND n.outcome<>'skipped' AND n.trusted_host=0)",
	}
	return `SELECT team,id FROM runs WHERE team=? AND repo_key=? AND provenance_sha=? AND pipeline=? AND intake_mode='execute' AND ` +
			q.provenRepository(provenance) + `=? AND ((` + trustIn(q.at(provenance, "trust"), runspec.GitHubCommitTrust()) + ` AND ` +
			q.at(provenance, "sha") + `=?) OR ` + q.at(provenance, "fetched_sha") + `=?) AND ` + strings.Join(clean, " AND "),
		[]any{team, string(repository), sha, pipeline, string(repository), sha, sha, team}, true
}

func restrictLatestRepositories(query string, args []any, repositories []string) (string, []any) {
	if len(repositories) == 0 {
		return query, args
	}
	query += " AND repo_display IN (" + strings.TrimSuffix(strings.Repeat("?,", len(repositories)), ",") + ")"
	for _, repository := range repositories {
		args = append(args, repository)
	}
	return query, args
}

type latestRunSelection uint8

const (
	latestRunObservation latestRunSelection = iota
	latestPipelineObservations
	latestReferencePin
)

// safety: authorization filters apply before ranking. Reference pins retain their
// finish-time policy because replacing a pin changes existing blob access.
func latestRunQuery(query string, selection latestRunSelection) string {
	const order = "COALESCE(started_at,created_at) DESC,number DESC,id DESC"
	switch selection {
	case latestReferencePin:
		return `WITH candidates AS (` + query + `) SELECT team,id FROM candidates ORDER BY finished_at DESC,current_pin DESC LIMIT 1`
	case latestPipelineObservations:
		return `SELECT team,id FROM (
            SELECT team,id,pipeline,COALESCE(started_at,created_at) AS activity_at,
                   ROW_NUMBER() OVER (PARTITION BY team,pipeline ORDER BY ` + order + `) AS latest_rank
            FROM runs WHERE team=? AND (team,id) IN (` + query + `)
        ) latest WHERE latest_rank=1 ORDER BY activity_at DESC,pipeline`
	default:
		return query + " ORDER BY " + order + " LIMIT 1"
	}
}

func advanceLatestSuccessPinTx(ctx context.Context, tx *storeTx, team Team, row RunRow, finished time.Time) error {
	if row.Request.Request.Mode == runspec.IntakePublish {
		return nil
	}
	query := latestRunQuery(`SELECT CAST(? AS TEXT) AS team,CAST(? AS TEXT) AS id,CAST(? AS BIGINT) AS finished_at,0 AS current_pin
        UNION ALL SELECT team,run_id AS id,finished_at,1 AS current_pin FROM pipeline_success_pins WHERE team=? AND pipeline=?`, latestReferencePin)
	var selectedTeam Team
	var selected string
	if err := tx.QueryRowContext(ctx, query, team, row.ID, finished.UnixNano(), team, row.Pipeline).Scan(&selectedTeam, &selected); err != nil {
		return err
	}
	if selected != row.ID {
		return nil
	}
	_, err := tx.execContext(ctx, `INSERT INTO pipeline_success_pins(team,pipeline,run_id,finished_at) VALUES (?,?,?,?) ON CONFLICT(team,pipeline) DO UPDATE SET run_id=excluded.run_id,finished_at=excluded.finished_at WHERE pipeline_success_pins.finished_at<excluded.finished_at`, team, row.Pipeline, row.ID, finished.UnixNano())
	return err
}

func latestSuccessPinTx(ctx context.Context, tx *storeTx, team Team, pipeline, run string) (bool, error) {
	query := latestRunQuery(`SELECT team,run_id AS id,finished_at,1 AS current_pin FROM pipeline_success_pins WHERE team=? AND pipeline=?`, latestReferencePin)
	var selectedTeam Team
	var selected string
	err := tx.QueryRowContext(ctx, query, team, pipeline).Scan(&selectedTeam, &selected)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return selected == run, err
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
	err = tx.QueryRowContext(ctx, latestRunQuery(query, latestRunObservation), args...).Scan(&team, &id)
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
