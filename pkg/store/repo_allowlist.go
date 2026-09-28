package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// RepoFilter decides which repositories a claim may take work from.
type RepoFilter = match.RepoFilter

type claimProfileKey struct{}

// WithClaimProfile carries what a claimant reported about itself into the
// trigger and node claims made under ctx: its observed platform and capacity
// and the repositories it accepts. The store overwrites the granted fields,
// name and class, from the credential, and replaces the labels with the ones
// the claim sent less the keys a claimant may not assert.
//
// An Accept filter admits a trigger only when the trigger names a repository
// the filter admits, and a node when its run names none or one it admits. A
// context without a profile claims every repository, as a runner that sends
// no list always has; such a runner refuses a disallowed run itself.
func WithClaimProfile(ctx context.Context, profile match.Profile) context.Context {
	return context.WithValue(ctx, claimProfileKey{}, profile)
}

// safety: the granted fields come off the credential, never off the context,
// so nothing a claimant sent can name it or raise its class.
func claimProfileFrom(ctx context.Context, claimant ClaimIdentity, labels []string) match.Profile {
	p, _ := ctx.Value(claimProfileKey{}).(match.Profile)
	p.Name = ""
	if name, ok := strings.CutPrefix(claimant.Principal, agentPrincipalPrefix); ok {
		p.Name = name
	}
	p.Class = match.ClassAgent
	p.Location = ""
	p.Labels = match.SelfAsserted(labels)
	return p
}

const agentPrincipalPrefix = "agent:"

// safety: the metered pool runs every node on the Cloud runner image, so it
// has exactly the tools that image declares, whatever tools the claim sent.
func withCloudTools(p match.Profile) match.Profile {
	labels := slices.DeleteFunc(slices.Clone(p.Labels), func(l string) bool { return strings.HasPrefix(l, match.ToolPrefix) })
	p.Labels = append(labels, match.ToolLabels(match.CloudTools)...)
	return p
}

func triggerRunRepository(t *Trigger, envJSON []byte) match.Repository {
	env, decoded := decodeTriggerEnv(envJSON)
	return match.Repository{
		URL: t.RepoURL, GitHubRepository: env["GITHUB_REPOSITORY"],
		GitHubOwner: t.GithubOwner, GitHubRepo: t.GithubRepo, Undecodable: !decoded,
	}
}

// runRepositoryOf reads the repository fields of runID's trigger within
// scope's team. A run with no trigger names none.
func (s *Store) runRepositoryOf(ctx context.Context, scope teamScope, runID string, seen map[string]match.Repository) (match.Repository, error) {
	if repo, ok := seen[runID]; ok {
		return repo, nil
	}
	if scope.all || scope.team == "" {
		return match.Repository{}, ErrClaimantHasNoTeam
	}
	repo, err := scanRunRepository(s.queryRow(ctx, runRepositorySQL, runID, string(scope.team)))
	if err != nil {
		return match.Repository{}, err
	}
	seen[runID] = repo
	return repo, nil
}

const runRepositorySQL = `SELECT repo_url, github_owner, github_repo, trigger_env
		FROM triggers WHERE id = ? AND team = ?`

func scanRunRepository(row *sql.Row) (match.Repository, error) {
	var t Trigger
	var envJSON []byte
	err := row.Scan(&t.RepoURL, &t.GithubOwner, &t.GithubRepo, &envJSON)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return match.Repository{}, nil
	case err != nil:
		return match.Repository{}, err
	}
	return triggerRunRepository(&t, envJSON), nil
}
