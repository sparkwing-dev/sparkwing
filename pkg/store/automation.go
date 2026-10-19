package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func automatedRun(r runspec.Request) bool {
	return r.Automation.Set
}

// safety: the automation identity comes only from a verified delivery, the schedule being fired, or the parent run;
// a request's own value is discarded, and retries keep the sealed one.
func automationTx(ctx context.Context, tx *storeTx, team Team, input runspec.Request) (runspec.Optional[runspec.Automation], error) {
	var identity runspec.Automation
	switch {
	case input.Origin == runspec.OriginApp && input.Git.App.Set:
		app := input.Git.App.Value
		identity.SenderID, identity.AuthorID = app.SenderID, app.AuthorID
		identity.Pushed = app.Event == "push" || app.Event == "pull_request" && (app.Action == "opened" || app.Action == "synchronize")
	case input.Origin == runspec.OriginSchedule && tx.scheduleAdmission != nil:
		identity.ArmedBy = tx.scheduleAdmission.schedule.ArmedBy
	case input.Origin == runspec.OriginChild && input.Lineage.ParentRunID != "":
		parent, err := sealedRequestTx(ctx, tx, team, input.Lineage.ParentRunID)
		if err != nil || !parent.Request.Automation.Set {
			return runspec.Optional[runspec.Automation]{}, err
		}
		identity = parent.Request.Automation.Value
		identity.Repos = slices.Clone(identity.Repos)
	default:
		return runspec.Optional[runspec.Automation]{}, nil
	}
	repos, err := sealedRepoKeysTx(ctx, tx, team, input.Git.Repository.Key)
	if err != nil {
		return runspec.Optional[runspec.Automation]{}, err
	}
	for _, repo := range repos {
		if !slices.Contains(identity.Repos, repo) {
			identity.Repos = append(identity.Repos, repo)
		}
	}
	return runspec.Optional[runspec.Automation]{Set: true, Value: identity}, nil
}

// safety: a repository without a gh:<id> key is kept as is, so the access check, which needs an ID, refuses it.
func sealedRepoKeysTx(ctx context.Context, tx *storeTx, team Team, key runspec.RepoKey) ([]runspec.RepoKey, error) {
	if key == "" {
		return nil, nil
	}
	repos := []runspec.RepoKey{key}
	raw, keyed := strings.CutPrefix(string(key), "gh:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !keyed || err != nil || id <= 0 {
		return repos, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT extra_repo_id FROM github_app_extra_repos WHERE team=? AND repository_id=? ORDER BY extra_repo`, team, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var extra int64
		if err := rows.Scan(&extra); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		repos = append(repos, runspec.RepoKey("gh:"+strconv.FormatInt(extra, 10)))
	}
	return repos, errors.Join(rows.Err(), rows.Close())
}

// safety: an App run belongs to a member only when that member pushed the code it runs and wrote it.
func automationOwnerTx(ctx context.Context, tx *storeTx, team Team, identity runspec.Automation) (string, error) {
	var account string
	var err error
	switch {
	case identity.ArmedBy != "":
		err = tx.QueryRowContext(ctx, `SELECT account_id FROM memberships WHERE team=? AND account_id=?`, team, identity.ArmedBy).Scan(&account)
	case identity.Pushed && identity.AuthorID > 0 && identity.SenderID == identity.AuthorID:
		err = tx.QueryRowContext(ctx, `SELECT i.account_id FROM identities i JOIN memberships m ON m.account_id=i.account_id AND m.team=? WHERE i.provider=? AND i.subject=?`, team, ProviderGitHub, strconv.FormatInt(identity.AuthorID, 10)).Scan(&account)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return account, err
}

// safety: the only rule that admits a personal machine to an automated run: the run's sealed owner is the machine's
// owner, an admin approved the machine, and a fresh check found the owner can read every sealed repository.
func personalAutomatedRunTx(ctx context.Context, tx *storeTx, team Team, prefix, owner string, identity runspec.Automation) error {
	refused := authorityError(apierr.Forbidden, "personal machine takes only its owner's automated runs after admin approval")
	account, err := automationOwnerTx(ctx, tx, team, identity)
	if err != nil {
		return err
	}
	if account == "" || account != owner || len(identity.Repos) == 0 {
		return refused
	}
	var approved int64
	err = tx.QueryRowContext(ctx, `SELECT approved_at FROM machine_automation WHERE team=? AND prefix=? AND owner=?`, team, prefix, owner).Scan(&approved)
	if errors.Is(err, sql.ErrNoRows) || err == nil && approved <= 0 {
		return refused
	}
	if err != nil {
		return err
	}
	for _, repo := range identity.Repos {
		var allowed int64
		err := tx.QueryRowContext(ctx, `SELECT allowed FROM machine_repo_access WHERE team=? AND prefix=? AND repo=? AND checked_at>?`, team, prefix, repo, tx.now().Add(-limits.PersonalRepoAccess).UnixNano()).Scan(&allowed)
		if errors.Is(err, sql.ErrNoRows) || err == nil && allowed != 1 {
			return refused
		}
		if err != nil {
			return err
		}
	}
	return nil
}
