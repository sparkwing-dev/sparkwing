package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MaxGitHubAppExtraRepos bounds one source repository's extra repositories.
const MaxGitHubAppExtraRepos = 10

func (t *Tenant) setGitHubExtraReposTx(ctx context.Context, tx *storeTx, repository string, extras []string, ids map[string]int64, by string) ([]string, error) {
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
	if len(list) != 0 {
		var installation int64
		if err := tx.QueryRowContext(ctx, `SELECT installation_id FROM github_app_installations WHERE team=? AND LOWER(account_login)=? AND suspended=0 ORDER BY installation_id LIMIT 1`+tx.forUpdate(), t.team, strings.ToLower(repo.Owner)).Scan(&installation); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
	}
	canonical := make(map[string]int64, len(ids))
	for slug, id := range ids {
		key := strings.ToLower(slug)
		if id <= 0 || key != source && !slices.Contains(list, key) || canonical[key] != 0 {
			return nil, ErrInvalidInput
		}
		canonical[key] = id
	}
	if canonical[source] <= 0 {
		return nil, ErrInvalidInput
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_app_extra_repos WHERE team = ? AND repository_id = ?`,
		string(t.team), canonical[source]); err != nil {
		return nil, err
	}
	for _, x := range list {
		if canonical[x] <= 0 {
			return nil, ErrInvalidInput
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO github_app_extra_repos (team, repository, repository_id, extra_repo, extra_repo_id, created_by, created_at)
   VALUES (?, ?, ?, ?, ?, ?, ?)`, string(t.team), source, canonical[source], x, canonical[x], by, tx.now().UnixNano()); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// GitHubAppExtraRepos returns approved extras for a numeric source repository ID.
func (t *Tenant) GitHubAppExtraRepos(ctx context.Context, repositoryID int64) (_ []string, err error) {
	rows, err := t.s.query(ctx, `
		SELECT extra_repo FROM github_app_extra_repos WHERE team = ? AND repository_id = ? ORDER BY extra_repo`,
		string(t.team), repositoryID)
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
