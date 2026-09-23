package store

import (
	"context"
	"database/sql"
	"errors"
)

// RepoFilter decides which repositories a claim may take work from.
// AdmitsRepository reads the fields a trigger names its repository with and
// reports whether they name one and whether the filter admits it; fields that
// disagree or do not parse name one that no filter admits.
type RepoFilter interface {
	AdmitsRepository(repoURL, githubRepository, githubOwner, githubRepo string) (named, admitted bool)
}

type repoFilterKey struct{}

// WithRepoFilter confines the trigger and node claims made under ctx to work
// whose repository filter admits, which is how a runner that fetches source
// with its own credentials claims only what its owner lets it build. A context
// without a filter claims every repository, as a runner that sends no list
// always has; such a runner refuses a disallowed run itself.
//
// A trigger is admitted only when it names a repository the filter admits: a
// trigger naming none has no source such a runner could fetch. A node is
// admitted when its run names no repository, since it then runs without
// fetching source, or names one the filter admits.
func WithRepoFilter(ctx context.Context, filter RepoFilter) context.Context {
	return context.WithValue(ctx, repoFilterKey{}, filter)
}

// RepoFilterFrom returns the filter [WithRepoFilter] set on ctx.
func RepoFilterFrom(ctx context.Context) (RepoFilter, bool) {
	filter, ok := ctx.Value(repoFilterKey{}).(RepoFilter)
	return filter, ok && filter != nil
}

// runRepository is the repository fields of a run's trigger.
type runRepository struct {
	repoURL, githubRepository, githubOwner, githubRepo string
	// safety: an environment that does not decode cannot be shown to name an
	// allowed repository, so no filter admits it.
	undecodable bool
}

func triggerRunRepository(t *Trigger, envJSON []byte) runRepository {
	env, decoded := decodeTriggerEnv(envJSON)
	return runRepository{
		repoURL: t.RepoURL, githubRepository: env["GITHUB_REPOSITORY"],
		githubOwner: t.GithubOwner, githubRepo: t.GithubRepo, undecodable: !decoded,
	}
}

func (r runRepository) admittedBy(filter RepoFilter) (named, admitted bool) {
	if r.undecodable {
		return true, false
	}
	return filter.AdmitsRepository(r.repoURL, r.githubRepository, r.githubOwner, r.githubRepo)
}

func triggerAdmittedBy(filter RepoFilter, t *Trigger, envJSON []byte) bool {
	named, admitted := triggerRunRepository(t, envJSON).admittedBy(filter)
	return named && admitted
}

func (r runRepository) admitsNodeFor(filter RepoFilter) bool {
	named, admitted := r.admittedBy(filter)
	return !named || admitted
}

// runRepositoryOf reads the repository fields of runID's trigger within
// scope's team. A run with no trigger names none.
func (s *Store) runRepositoryOf(ctx context.Context, scope teamScope, runID string, seen map[string]runRepository) (runRepository, error) {
	if repo, ok := seen[runID]; ok {
		return repo, nil
	}
	if scope.all || scope.team == "" {
		return runRepository{}, ErrClaimantHasNoTeam
	}
	var t Trigger
	var envJSON []byte
	err := s.queryRow(ctx, `SELECT repo_url, github_owner, github_repo, trigger_env
		FROM triggers WHERE id = ? AND team = ?`, runID, string(scope.team)).
		Scan(&t.RepoURL, &t.GithubOwner, &t.GithubRepo, &envJSON)
	var repo runRepository
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return runRepository{}, err
	default:
		repo = triggerRunRepository(&t, envJSON)
	}
	seen[runID] = repo
	return repo, nil
}
