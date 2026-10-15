package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// RepoDispatch names the path a repository's new runs take.
type RepoDispatch string

const (
	// RepoDispatchTrigger is every repository's default: a runner claims the
	// run's trigger and dispatches its nodes. A repository with no setting,
	// or one stored empty, takes it too.
	RepoDispatchTrigger RepoDispatch = "trigger"
	// RepoDispatchController starts each run with a planning node, and the
	// launcher runs every node in a Job it builds itself.
	RepoDispatchController RepoDispatch = "controller"
)

// RepoKey is the key a repository's settings are stored under: its GitHub
// owner and name, lowercased because GitHub resolves both case-insensitively.
func RepoKey(owner, name string) string {
	return strings.ToLower(owner + "/" + name)
}

// safety: runs in the transaction that inserts the trigger and its run, so a
// run of an opted-in repository is never claimable as a trigger, not even for
// the instant between two commits. The trigger stays as the intake record that
// idempotency keys and webhook replays read.
func routeRunDispatchTx(ctx context.Context, tx *storeTx, team Team, t Trigger, now time.Time) error {
	if t.GithubOwner == "" || t.GithubRepo == "" {
		return nil
	}
	var dispatch string
	err := tx.QueryRowContext(ctx, `SELECT dispatch FROM repos WHERE team = ? AND repo = ?`,
		string(team), RepoKey(t.GithubOwner, t.GithubRepo)).Scan(&dispatch)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && dispatch != string(RepoDispatchController)) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE triggers SET status = ? WHERE team = ? AND id = ?`,
		triggerStatusDone, string(team), t.ID); err != nil {
		return err
	}
	return insertPlanNodeTx(ctx, tx, team, t.ID, RepoDispatch(dispatch), now)
}
