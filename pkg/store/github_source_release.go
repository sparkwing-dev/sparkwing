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
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// GitHubSourceProvider verifies coverage and mints repository-ID-scoped read tokens.
type GitHubSourceProvider interface {
	InstallationTokenByIDs(context.Context, int64, []int64, map[string]string) (githubapp.Token, error)
}

// ReleaseGitHubSourceCredential rechecks live consent and commits an audit after minting outside database transactions.
func (t *Tenant) ReleaseGitHubSourceCredential(ctx context.Context, authorized capability.Authorized, provider GitHubSourceProvider) (source.Credential, error) {
	if provider == nil {
		return source.Credential{}, ErrNotFound
	}
	var before githubSourceRelease
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		before, err = t.githubSourceReleaseTx(ctx, tx, authorized)
		return err
	})
	if err != nil {
		return source.Credential{}, err
	}
	token, err := provider.InstallationTokenByIDs(ctx, before.installation, before.ids, map[string]string{"contents": "read"})
	if errors.Is(err, githubapp.ErrNotInstalled) {
		return source.Credential{}, authorityError(apierr.Conflict, "install the team's GitHub App on the approved repositories or update the owner's extras: "+strings.Join(before.names, ", "))
	}
	if err != nil {
		return source.Credential{}, errors.Join(authorityError(apierr.Unavailable, "GitHub source token mint failed"), err)
	}
	var credential source.Credential
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		current, err := t.githubSourceReleaseTx(ctx, tx, authorized)
		if err != nil {
			return err
		}
		if current.installation != before.installation || !slices.Equal(current.ids, before.ids) || !slices.Equal(current.names, before.names) {
			return bindingRefusal()
		}
		if token.Token == "" || !token.ExpiresAt.After(tx.now()) {
			return authorityError(apierr.Unavailable, "GitHub returned no live source token")
		}
		audit, err := capability.AuthorizeSourceRelease(current.request, tx.now())
		if err != nil {
			return err
		}
		if err := insertSourceReleaseAuditTx(ctx, tx, audit); err != nil {
			return err
		}
		credential = source.Credential{Kind: "github_app", Host: "github.com", Secret: token.Token, ExtraRepositories: slices.Clone(current.names[1:])}
		return nil
	})
	if err != nil {
		return source.Credential{}, err
	}
	return credential, nil
}

type githubSourceRelease struct {
	request      capability.SourceReleaseRequest
	installation int64
	ids          []int64
	names        []string
}

func (t *Tenant) githubSourceReleaseTx(ctx context.Context, tx *storeTx, authorized capability.Authorized) (githubSourceRelease, error) {
	var out githubSourceRelease
	request, err := t.sourceReleaseRequestTx(ctx, tx, authorized)
	if err != nil {
		return out, err
	}
	if request.Host != "github.com" {
		return out, ErrNotFound
	}
	run, err := readRunTx(ctx, tx, t.team, request.Authorization.Binding.RunID)
	if err != nil {
		return out, err
	}
	chain, err := authorityChainTx(ctx, tx, authorized.Principal.Authority, true)
	if err != nil {
		return out, err
	}
	request.Executor, err = t.sourceExecutorTx(ctx, tx, chain, run, false)
	if err != nil {
		return out, err
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(request.RepositoryKey, "gh:"), 10, 64)
	if err != nil || id <= 0 {
		return out, bindingRefusal()
	}
	installation, slug, err := t.githubSourceInstallationTx(ctx, tx, run.Request, id)
	if err != nil {
		return out, err
	}
	ids, names, err := t.githubSourceRepositoriesTx(ctx, tx, slug, id)
	if err != nil {
		return out, err
	}
	request.GitHubApp, request.CredentialID = true, fmt.Sprintf("github_app:%d", installation)
	if _, err := capability.AuthorizeSourceRelease(request, tx.now()); err != nil {
		return out, err
	}
	return githubSourceRelease{request, installation, ids, names}, nil
}

func githubSourceMissing(remedy string) error {
	return errors.Join(ErrNotFound, &apierr.Error{Code: apierr.NotFound, Message: remedy + " or store a credential in the team's Git credentials"})
}

func (t *Tenant) githubSourceInstallationTx(ctx context.Context, tx *storeTx, sealed runspec.SealedRequest, id int64) (int64, string, error) {
	request := sealed.Request
	var installation int64
	var slug string
	scheduled := false
	scheduleQuery := `SELECT a.installation_id,a.repository FROM cron_schedules c JOIN github_app_automation a ON a.team=c.team AND a.installation_id=c.github_installation_id AND a.repository_id=c.github_repository_id WHERE c.team=? AND c.id=? AND c.repo_key=? AND c.github_repository_id=? AND c.pipeline=?`
	scheduleArgs := []any{t.team, sealed.AuthorityRef.ID, request.Git.Repository.Key, id, request.Pipeline}
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
		if errors.Is(err, sql.ErrNoRows) && sealed.AuthorityRef.Kind == "schedule" && request.Origin == runspec.OriginSchedule {
			err = tx.QueryRowContext(ctx, scheduleQuery, scheduleArgs...).Scan(&installation, &slug)
			scheduled = err == nil
		}
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
	if err == nil && scheduled {
		err = tx.QueryRowContext(ctx, scheduleQuery+tx.forShare(), scheduleArgs...).Scan(&installation, &slug)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", githubSourceMissing("restore the schedule's repository automation consent")
		}
		if err == nil && installation != current {
			return 0, "", bindingRefusal()
		}
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
