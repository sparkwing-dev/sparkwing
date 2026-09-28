package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RepoDispatch names the path a repository's new runs take.
type RepoDispatch string

const (
	// RepoDispatchTrigger is every repository's default: a runner claims the
	// run's trigger and dispatches its nodes.
	RepoDispatchTrigger RepoDispatch = ""
	// RepoDispatchController starts each run with a planning node, and the
	// launcher runs every node in a Job it builds itself.
	RepoDispatchController RepoDispatch = "controller"
)

const reposTable = `CREATE TABLE IF NOT EXISTS repos (
    team       TEXT NOT NULL,
    repo       TEXT NOT NULL,
    dispatch   TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (team, repo)
)`

var repoDispatchRunCols = map[string]string{
	"dispatch": "TEXT NOT NULL DEFAULT ''",
}

var launchNodeCols = map[string]string{
	"timeout_ms": "INTEGER NOT NULL DEFAULT 0",
}

func applyRepoDispatchMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	ddl := reposTable
	if postgres {
		ddl = strings.ReplaceAll(ddl, "INTEGER", "BIGINT")
	}
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	return addDispatchColumnsTx(ctx, tx, postgres, map[string]map[string]string{
		"runs": repoDispatchRunCols, "nodes": launchNodeCols,
	})
}

// RepoKey is the key a repository's settings are stored under: its GitHub
// owner and name, lowercased because GitHub resolves both case-insensitively.
func RepoKey(owner, name string) string {
	return strings.ToLower(owner + "/" + name)
}

// ErrControllerDispatchIncomplete refuses opting a repository into
// [RepoDispatchController] while its pods can neither fetch claim-bound source
// nor report through claim tokens, so no run of it could finish.
var ErrControllerDispatchIncomplete = errors.New(
	"store: controller dispatch cannot run a pipeline yet: pods do not fetch source or report through claim tokens")

// SetRepoDispatch sets the path new runs of the team's repository owner/name
// take. Runs already started keep the path they started on. It refuses
// [RepoDispatchController] with [ErrControllerDispatchIncomplete] for now.
func (t *Tenant) SetRepoDispatch(ctx context.Context, owner, name string, dispatch RepoDispatch, now time.Time) error {
	switch dispatch {
	case RepoDispatchTrigger:
	case RepoDispatchController:
		return ErrControllerDispatchIncomplete
	default:
		return fmt.Errorf("%w: dispatch %q is neither %q nor empty", ErrInvalidInput, dispatch, RepoDispatchController)
	}
	if owner == "" || name == "" || strings.Contains(owner+name, "/") {
		return fmt.Errorf("%w: a repository is a GitHub owner and name", ErrInvalidInput)
	}
	_, err := t.s.exec(ctx, `INSERT INTO repos (team, repo, dispatch, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (team, repo) DO UPDATE SET dispatch = excluded.dispatch, updated_at = excluded.updated_at`,
		string(t.team), RepoKey(owner, name), string(dispatch), now.UnixNano())
	return err
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
	if errors.Is(err, sql.ErrNoRows) || (err == nil && dispatch == string(RepoDispatchTrigger)) {
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
