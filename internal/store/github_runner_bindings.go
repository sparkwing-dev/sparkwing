package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// GitHubRunnerBinding is a team owner's consent that workflow jobs of one
// GitHub repository may run that team's work for that repository.
type GitHubRunnerBinding struct {
	Team Team
	// RepositoryID is GitHub's numeric id, the key a job's ID token is
	// matched on, because a repository name can be renamed or re-registered
	// by someone else.
	RepositoryID int64
	// RepositoryOwnerID pins the account that owned the repository when the
	// binding was made, so a transferred repository stops matching.
	RepositoryOwnerID int64
	// Repository is "owner/name" as the owner entered it, for display.
	Repository string
	// InstallationID is the team's GitHub App installation that covered the
	// repository at enrollment; the binding ends with it. Zero on a
	// controller without the App.
	InstallationID int64
	CreatedBy  string
	CreatedAt  time.Time
}

// GitHubRunnerBindings lists t's bindings, oldest first.
func (t *Tenant) GitHubRunnerBindings(ctx context.Context) (_ []GitHubRunnerBinding, err error) {
	rows, err := t.s.query(ctx, `
		SELECT repository_id, repository_owner_id, repository, COALESCE(installation_id, 0), created_by, created_at
		FROM github_runner_bindings WHERE team = ? ORDER BY created_at, repository_id`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []GitHubRunnerBinding
	for rows.Next() {
		b := GitHubRunnerBinding{Team: t.team}
		var created int64
		if err := rows.Scan(&b.RepositoryID, &b.RepositoryOwnerID, &b.Repository, &b.InstallationID, &b.CreatedBy, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// GitHubRunnerBinding reads t's binding for a repository id, or ErrNotFound.
func (t *Tenant) GitHubRunnerBinding(ctx context.Context, repositoryID int64) (GitHubRunnerBinding, error) {
	b := GitHubRunnerBinding{Team: t.team, RepositoryID: repositoryID}
	var created int64
	err := t.s.queryRow(ctx, `
		SELECT repository_owner_id, repository, COALESCE(installation_id, 0), created_by, created_at
		FROM github_runner_bindings WHERE team = ? AND repository_id = ?`,
		string(t.team), repositoryID).Scan(&b.RepositoryOwnerID, &b.Repository, &b.InstallationID, &b.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubRunnerBinding{}, ErrNotFound
	}
	if err != nil {
		return GitHubRunnerBinding{}, err
	}
	b.CreatedAt = time.Unix(0, created).UTC()
	return b, nil
}

// maxGitHubRunnerBindings bounds the repositories one team binds to GitHub
// runners, funded or not.
const maxGitHubRunnerBindings = 20

// ErrBindingLimit refuses a GitHub runner binding past
// [maxGitHubRunnerBindings].
var ErrBindingLimit = errors.New("store: this team binds as many repositories as it may")

// ErrAlreadyBound is returned when a team binds a repository it already binds.
var ErrAlreadyBound = errors.New("store: this repository is already bound to the team")

// maxGitHubRunnerCredentials bounds the live credentials one team's GitHub
// Actions jobs hold at once, since each exchange is unauthenticated until
// the job's ID token verifies.
const maxGitHubRunnerCredentials = 20

// ErrGitHubRunnerCredentialLimit is returned when a team already holds
// [maxGitHubRunnerCredentials] live GitHub Actions credentials.
var ErrGitHubRunnerCredentialLimit = errors.New("store: this team already holds the maximum live GitHub Actions runner credentials")

// GitHubRepo identifies a repository on github.com. App deliveries supply ID;
// parsed slugs alone leave it zero.
type GitHubRepo struct {
	ID    int64
	Owner string
	Name  string
}

// ParseGitHubRepo reads "owner/name". Both parts are GitHub's character set:
// letters, digits, '-', '_' and '.'.
func ParseGitHubRepo(slug string) (GitHubRepo, bool) {
	owner, name, ok := strings.Cut(strings.TrimSpace(slug), "/")
	if !ok || !githubNamePart(owner) || !githubNamePart(name) || name == "." || name == ".." {
		return GitHubRepo{}, false
	}
	return GitHubRepo{Owner: owner, Name: name}, true
}

func githubNamePart(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// Slug is "owner/name".
func (r GitHubRepo) Slug() string { return r.Owner + "/" + r.Name }
