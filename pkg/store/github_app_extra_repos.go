package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// A team owner lists, per source repository, the further repositories of
// the same owner that a run's GitHub App token for that repository also
// reads, such as private submodules. The list lives here rather than in the
// repository, because code in the repository must not widen its own token.
const githubAppExtraReposTableSQLite = `CREATE TABLE IF NOT EXISTS github_app_extra_repos (
    team        TEXT NOT NULL,
    repository  TEXT NOT NULL,
    extra_repo  TEXT NOT NULL,
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (team, repository, extra_repo)
);`

var githubAppExtraReposTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(githubAppExtraReposTableSQLite)

// MaxGitHubAppExtraRepos bounds one source repository's extra repositories.
const MaxGitHubAppExtraRepos = 10

// SetGitHubAppExtraRepos replaces the extra repositories a run of
// repository's App token also reads, each an owner/name of repository's
// owner, at most [MaxGitHubAppExtraRepos]. An empty list clears them.
func (t *Tenant) SetGitHubAppExtraRepos(ctx context.Context, repository string, extras []string, by string, now time.Time) (_ []string, err error) {
	repo, ok := ParseGitHubRepo(repository)
	if !ok {
		return nil, fmt.Errorf("%w: repository %q is not owner/name", ErrInvalidInput, repository)
	}
	source := strings.ToLower(repo.Slug())
	var list []string
	for _, raw := range extras {
		x, ok := ParseGitHubRepo(raw)
		if !ok {
			return nil, fmt.Errorf("%w: extra repository %q is not owner/name", ErrInvalidInput, raw)
		}
		if !strings.EqualFold(x.Owner, repo.Owner) {
			return nil, fmt.Errorf("%w: extra repository %s is not of %s, the repository's owner", ErrInvalidInput, x.Slug(), repo.Owner)
		}
		slug := strings.ToLower(x.Slug())
		if slug != source && !slices.Contains(list, slug) {
			list = append(list, slug)
		}
	}
	if len(list) > MaxGitHubAppExtraRepos {
		return nil, fmt.Errorf("%w: at most %d extra repositories", ErrInvalidInput, MaxGitHubAppExtraRepos)
	}
	slices.Sort(list)
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackUnlessDone(tx, &err)
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_app_extra_repos WHERE team = ? AND repository = ?`,
		string(t.team), source); err != nil {
		return nil, err
	}
	for _, x := range list {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO github_app_extra_repos (team, repository, extra_repo, created_by, created_at)
			VALUES (?, ?, ?, ?, ?)`, string(t.team), source, x, by, now.UTC().Unix()); err != nil {
			return nil, err
		}
	}
	return list, tx.Commit()
}

// GitHubAppExtraRepos is the extra repositories an owner listed for
// repository, sorted; empty when none.
func (t *Tenant) GitHubAppExtraRepos(ctx context.Context, repository string) (_ []string, err error) {
	rows, err := t.s.query(ctx, `
		SELECT extra_repo FROM github_app_extra_repos WHERE team = ? AND repository = ? ORDER BY extra_repo`,
		string(t.team), strings.ToLower(repository))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []string{}
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// AllGitHubAppExtraRepos maps each of the team's source repositories to the
// extra repositories an owner listed for it.
func (t *Tenant) AllGitHubAppExtraRepos(ctx context.Context) (_ map[string][]string, err error) {
	rows, err := t.s.query(ctx, `
		SELECT repository, extra_repo FROM github_app_extra_repos WHERE team = ? ORDER BY repository, extra_repo`,
		string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := map[string][]string{}
	for rows.Next() {
		var repo, x string
		if err := rows.Scan(&repo, &x); err != nil {
			return nil, err
		}
		out[repo] = append(out[repo], x)
	}
	return out, rows.Err()
}
