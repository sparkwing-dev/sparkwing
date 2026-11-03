package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// TrustedRunners names, per repository, the runner subjects an operator trusts
// to publish results a trusted reference may read. It grants nothing else.
type TrustedRunners map[runspec.RepoKey][]capability.Subject

// ParseTrustedRunners reads the operator's --trusted-gate-runners value:
// semicolon-separated <repo_key>=machine:<id>[,machine:<id>]... entries, where
// repo_key is a gh: or git: repository key. An empty value trusts no runner.
func ParseTrustedRunners(spec string) (TrustedRunners, error) {
	trusted := TrustedRunners{}
	for _, entry := range strings.Split(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		repo, subjects, ok := strings.Cut(entry, "=")
		key := runspec.RepoKey(strings.TrimSpace(repo))
		if !ok || runspec.ValidateRepoUse(key, runspec.RepoAccessRule) != nil {
			return nil, fmt.Errorf("trusted gate runners: %q needs a gh: or git: repository key before =", entry)
		}
		for _, subject := range strings.Split(subjects, ",") {
			kind, id, ok := strings.Cut(strings.TrimSpace(subject), ":")
			if !ok || capability.SubjectKind(kind) != capability.SubjectMachine || id == "" {
				return nil, fmt.Errorf("trusted gate runners: %q is not a machine:<id> runner subject", subject)
			}
			trusted[key] = append(trusted[key], capability.Subject{Kind: capability.SubjectMachine, ID: id})
		}
	}
	return trusted, nil
}

// SetTrustedRunners replaces the operator's trusted runners; nil trusts none.
func (s *Store) SetTrustedRunners(trusted TrustedRunners) { s.trustedRunners.Store(&trusted) }

func (s *Store) trusted() TrustedRunners {
	if trusted := s.trustedRunners.Load(); trusted != nil {
		return *trusted
	}
	return nil
}

// GetLatestTrustedRun returns the newest run of pipeline at the reading run's
// own commit that either proved that commit in the reader's proven repository,
// or was published by a runner the operator trusts for the reader's repository.
// Returns ErrNotFound when no run qualifies.
func (t *Tenant) GetLatestTrustedRun(ctx context.Context, pipeline, readerRunID string, statuses []string, maxAge time.Duration) (*Run, error) {
	if t.team == "" {
		return nil, ErrNoTeam
	}
	reader, err := t.ReadRun(ctx, readerRunID)
	if err != nil {
		return nil, err
	}
	query, args, ok := trustedRunQuery(t.s.dialect, t.team, pipeline, reader.Request, t.s.trusted())
	if !ok {
		return nil, notFound("pipeline run", pipeline)
	}
	return t.s.getLatestRun(ctx, query, args, pipeline, statuses, maxAge, true)
}

// safety: the output read after a trusted lookup re-proves the run, so a run
// id the reader learned elsewhere cannot borrow the trusted reference.
func trustedForReaderTx(ctx context.Context, tx *storeTx, team Team, readerRunID string, candidate RunRow) (bool, error) {
	reader, err := readRunTx(ctx, tx, team, readerRunID)
	if err != nil {
		return false, err
	}
	query, args, ok := trustedRunQuery(tx.dialect, team, candidate.Pipeline, reader.Request, tx.store.trusted())
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

// safety: the commit comes from the reader's sealed request, never from the
// caller, and an asserted run proves no repository, so it matches only through
// an operator-trusted publisher.
func trustedRunQuery(dialect Dialect, team Team, pipeline string, reader runspec.SealedRequest, trusted TrustedRunners) (string, []any, bool) {
	q := claimSQL{dialect: dialect}
	provenance := q.at(q.blob("request_json"), "provenance")
	var clauses []string
	var args []any
	if repository, sha := reader.Provenance.ProvenRepository(), reader.Provenance.ProvenSHA(); repository != "" && sha != "" {
		clauses = append(clauses, "(intake_mode='execute' AND "+q.provenRepository(provenance)+"=? AND (("+
			trustIn(q.at(provenance, "trust"), runspec.GitHubCommitTrust)+" AND "+q.at(provenance, "sha")+"=?) OR "+
			q.at(provenance, "fetched_sha")+"=?))")
		args = append(args, string(repository), sha, sha)
	}
	sha := reader.Provenance.ProvenSHA()
	if sha == "" {
		sha = strings.ToLower(reader.Request.Git.SHA)
	}
	if subjects := trusted[reader.Request.Git.Repository.Key]; len(subjects) > 0 && sha != "" {
		publishers := make([]string, len(subjects))
		args = append(args, sha)
		for i, subject := range subjects {
			publishers[i] = "(created_subject_kind=? AND created_principal=?)"
			args = append(args, string(subject.Kind), subject.ID)
		}
		clauses = append(clauses, "(intake_mode='publish' AND git_sha=? AND ("+strings.Join(publishers, " OR ")+"))")
	}
	if len(clauses) == 0 {
		return "", nil, false
	}
	return `SELECT team,id FROM runs WHERE team=? AND pipeline=? AND (` + strings.Join(clauses, " OR ") + `)`,
		append([]any{team, pipeline}, args...), true
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
