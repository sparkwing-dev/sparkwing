package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// GitHubSourceProvider verifies coverage and mints repository-ID-scoped read tokens.
type GitHubSourceProvider interface {
	InstallationRepositories(context.Context, int64) ([]githubapp.Repository, error)
	InstallationTokenByIDs(context.Context, int64, []int64, map[string]string) (githubapp.Token, error)
}

// ReleaseGitHubSourceCredential commits live claim and owner-consent checks before returning a token.
func (t *Tenant) ReleaseGitHubSourceCredential(ctx context.Context, authorized capability.Authorized, provider GitHubSourceProvider) (githubapp.Token, error) {
	var token githubapp.Token
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		request, err := t.sourceReleaseRequestTx(ctx, tx, authorized)
		if err != nil {
			return err
		}
		if request.Host != "github.com" || provider == nil {
			return ErrNotFound
		}
		run, err := readRunTx(ctx, tx, t.team, request.Authorization.Binding.RunID)
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(request.RepositoryKey, "gh:"), 10, 64)
		if err != nil || id <= 0 {
			return bindingRefusal()
		}
		installation, slug, err := t.githubSourceInstallationTx(ctx, tx, run.Request.Request, id)
		if err != nil {
			return err
		}
		ids, names, err := t.githubSourceRepositoriesTx(ctx, tx, slug, id)
		if err != nil {
			return err
		}
		repositories, err := provider.InstallationRepositories(ctx, installation)
		if err != nil && !errors.Is(err, githubapp.ErrTooManyRepositories) {
			return err
		}
		for i, required := range ids {
			if !slices.ContainsFunc(repositories, func(repo githubapp.Repository) bool { return repo.ID == required }) {
				if i == 0 {
					return githubSourceMissing("install the team's GitHub App on " + slug)
				}
				return &apierr.Error{Code: apierr.Conflict, Message: "the installation no longer covers " + names[i]}
			}
		}
		request.GitHubApp, request.CredentialID = true, fmt.Sprintf("github_app:%d", installation)
		if _, err := capability.AuthorizeSourceRelease(request, tx.now()); err != nil {
			return err
		}
		token, err = provider.InstallationTokenByIDs(ctx, installation, ids, map[string]string{"contents": "read"})
		if err != nil {
			return err
		}
		if token.Token == "" || !token.ExpiresAt.After(tx.now()) {
			return authorityError(apierr.Unavailable, "GitHub returned no live source token")
		}
		if _, err := authorityChainTx(ctx, tx, authorized.Principal.Authority, false); err != nil {
			return err
		}
		if err := checkSourceReleaseFenceTx(ctx, tx, request.Authorization.Principal.Capability); err != nil {
			return err
		}
		audit, err := capability.AuthorizeSourceRelease(request, tx.now())
		if err != nil {
			return err
		}
		return insertSourceReleaseAuditTx(ctx, tx, audit)
	})
	if err != nil {
		return githubapp.Token{}, err
	}
	return token, nil
}

func githubSourceMissing(remedy string) error {
	return errors.Join(ErrNotFound, &apierr.Error{Code: apierr.NotFound, Message: remedy + " or store a credential in the team's Git credentials"})
}

func (t *Tenant) githubSourceInstallationTx(ctx context.Context, tx *storeTx, request runspec.Request, id int64) (int64, string, error) {
	var installation int64
	var slug string
	app := request.Git.App
	if app.Set {
		if app.Value.RepositoryID != id || app.Value.VerifiedAt.IsZero() || app.Value.SHA != request.Git.SHA || app.Value.Ref != request.Git.Ref {
			return 0, "", bindingRefusal()
		}
		installation = app.Value.InstallationID
		identity, err := sourceurl.Identity(request.Source.Location.RemoteURL)
		if err != nil {
			return 0, "", err
		}
		repo, ok := ParseGitHubRepo(strings.TrimPrefix(identity, "github.com/"))
		if !ok {
			return 0, "", bindingRefusal()
		}
		slug = repo.Slug()
	} else {
		err := tx.QueryRowContext(ctx, `SELECT installation_id,repository FROM github_app_triggers WHERE team=? AND repository_id=? ORDER BY created_at LIMIT 1`+tx.forShare(), t.team, id).Scan(&installation, &slug)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", githubSourceMissing("a team owner subscribes it before the App can read a typed repository")
		}
		if err != nil {
			return 0, "", err
		}
	}
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT installation_id FROM github_app_installations WHERE team=? AND installation_id=? AND suspended=0`+tx.forUpdate(), t.team, installation).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", githubSourceMissing("install the team's GitHub App on " + slug)
	}
	return installation, strings.ToLower(slug), err
}

func (t *Tenant) githubSourceRepositoriesTx(ctx context.Context, tx *storeTx, slug string, id int64) ([]int64, []string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT extra_repo_id,extra_repo FROM github_app_extra_repos WHERE team=? AND repository_id=? ORDER BY extra_repo`+tx.forShare(), t.team, id)
	if err != nil {
		return nil, nil, err
	}
	defer closeRowsOrLog(rows)
	ids, names := []int64{id}, []string{slug}
	for rows.Next() {
		var extra int64
		var name string
		if err := rows.Scan(&extra, &name); err != nil {
			return nil, nil, err
		}
		if extra <= 0 {
			return nil, nil, bindingRefusal()
		}
		if !slices.Contains(ids, extra) {
			ids, names = append(ids, extra), append(names, name)
		}
	}
	return ids, names, rows.Err()
}
