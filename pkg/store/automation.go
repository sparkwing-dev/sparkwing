package store

import (
	"context"
	"database/sql"
	"errors"
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
		identity.SenderID, identity.AuthorID = input.Git.App.Value.SenderID, input.Git.App.Value.AuthorID
	case input.Origin == runspec.OriginSchedule && tx.scheduleAdmission != nil:
		identity.ArmedBy = tx.scheduleAdmission.schedule.ArmedBy
	case input.Origin == runspec.OriginChild && input.Lineage.ParentRunID != "":
		parent, err := sealedRequestTx(ctx, tx, team, input.Lineage.ParentRunID)
		return parent.Request.Automation, err
	default:
		return runspec.Optional[runspec.Automation]{}, nil
	}
	raw, keyed := strings.CutPrefix(string(input.Git.Repository.Key), "gh:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if keyed && err == nil && id > 0 {
		identity.Repos = append(identity.Repos, input.Git.Repository.Key)
		rows, err := tx.QueryContext(ctx, `SELECT extra_repo_id FROM github_app_extra_repos WHERE team=? AND repository_id=? ORDER BY extra_repo`, team, id)
		if err != nil {
			return runspec.Optional[runspec.Automation]{}, err
		}
		for rows.Next() {
			var extra int64
			if err := rows.Scan(&extra); err != nil {
				return runspec.Optional[runspec.Automation]{}, errors.Join(err, rows.Close())
			}
			identity.Repos = append(identity.Repos, runspec.RepoKey("gh:"+strconv.FormatInt(extra, 10)))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return runspec.Optional[runspec.Automation]{}, err
		}
	}
	return runspec.Optional[runspec.Automation]{Set: true, Value: identity}, nil
}

// safety: an App run belongs to a member only when its author is also its sender, so an email match never suffices.
func automationOwnerTx(ctx context.Context, tx *storeTx, team Team, identity runspec.Automation) (string, error) {
	var account string
	var err error
	switch {
	case identity.ArmedBy != "":
		err = tx.QueryRowContext(ctx, `SELECT account_id FROM memberships WHERE team=? AND account_id=?`, team, identity.ArmedBy).Scan(&account)
	case identity.AuthorID > 0 && identity.SenderID == identity.AuthorID:
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
