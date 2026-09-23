package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GitHubAppInstallation binds one installation of the deployment's GitHub
// App to the team whose owner connected it. An installation belongs to at
// most one team, because the installation is what proves control of the
// repositories it covers.
type GitHubAppInstallation struct {
	Team           Team
	InstallationID int64
	// AccountID and AccountLogin name the GitHub account the App is
	// installed on; AccountType is "User" or "Organization".
	AccountID    int64
	AccountLogin string
	AccountType  string
	Suspended    bool
	// ConnectedBy is the Sparkwing account that connected it, and
	// GitHubUserID the GitHub user that account proved it was.
	ConnectedBy  string
	GitHubUserID int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// GitHubAppTrigger subscribes one pipeline to one repository's events
// arriving through the App.
type GitHubAppTrigger struct {
	Team           Team
	RepositoryID   int64
	Repository     string
	InstallationID int64
	Pipeline       string
	Push           bool
	PullRequest    bool
	// ForkPullRequests admits pull requests from forks, which run
	// untrusted.
	ForkPullRequests bool
	CreatedBy        string
	CreatedAt        time.Time
}

// ErrInstallationBoundElsewhere is returned when a team connects an
// installation another team already holds.
var ErrInstallationBoundElsewhere = errors.New("store: this GitHub App installation is connected to another team")

const githubAppTablesSQLite = `
CREATE TABLE IF NOT EXISTS github_app_installations (
    installation_id INTEGER PRIMARY KEY,
    team            TEXT NOT NULL,
    account_id      INTEGER NOT NULL,
    account_login   TEXT NOT NULL,
    account_type    TEXT NOT NULL,
    suspended       INTEGER NOT NULL DEFAULT 0,
    connected_by    TEXT NOT NULL DEFAULT '',
    github_user_id  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_github_app_installations_team ON github_app_installations(team);
CREATE TABLE IF NOT EXISTS github_app_triggers (
    team               TEXT NOT NULL,
    repository_id      INTEGER NOT NULL,
    pipeline           TEXT NOT NULL,
    repository         TEXT NOT NULL,
    installation_id    INTEGER NOT NULL,
    on_push            INTEGER NOT NULL DEFAULT 0,
    on_pull_request    INTEGER NOT NULL DEFAULT 0,
    fork_pull_requests INTEGER NOT NULL DEFAULT 0,
    created_by         TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    PRIMARY KEY (team, repository_id, pipeline)
);
CREATE INDEX IF NOT EXISTS idx_github_app_triggers_installation ON github_app_triggers(installation_id, repository_id)
`

var githubAppTablesPostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(githubAppTablesSQLite)

var triggersUntrustedCols = map[string]string{"untrusted": "INTEGER NOT NULL DEFAULT 0"}

func applyGitHubAppMigrationSQLite(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(githubAppTablesSQLite) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return ensureColumnsSQLite(ctx, tx, "triggers", triggersUntrustedCols)
}

func applyGitHubAppMigrationPostgres(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(githubAppTablesPostgres) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return addColumnsTx(ctx, tx, "triggers", triggersUntrustedCols)
}

const githubAppInstallationCols = `installation_id, team, account_id, account_login, account_type, suspended,
       connected_by, github_user_id, created_at, updated_at`

func scanGitHubAppInstallation(row interface{ Scan(...any) error }) (GitHubAppInstallation, error) {
	var in GitHubAppInstallation
	var team string
	var suspended int
	var created, updated int64
	if err := row.Scan(&in.InstallationID, &team, &in.AccountID, &in.AccountLogin, &in.AccountType, &suspended,
		&in.ConnectedBy, &in.GitHubUserID, &created, &updated); err != nil {
		return GitHubAppInstallation{}, err
	}
	in.Team, in.Suspended = Team(team), suspended != 0
	in.CreatedAt, in.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return in, nil
}

// BindGitHubAppInstallation binds in to t, or refreshes the account and
// connector of a binding t already holds. An installation another team holds
// is [ErrInstallationBoundElsewhere], and the binding stays where it is.
func (t *Tenant) BindGitHubAppInstallation(ctx context.Context, in GitHubAppInstallation, now time.Time) (GitHubAppInstallation, error) {
	if in.InstallationID <= 0 || in.AccountID <= 0 || in.AccountLogin == "" || in.AccountType == "" {
		return GitHubAppInstallation{}, fmt.Errorf("%w: a binding needs the installation and its account", ErrInvalidInput)
	}
	at := now.UTC().Unix()
	suspended := 0
	if in.Suspended {
		suspended = 1
	}
	// safety: the conflict guard compares teams, so a second team's connect
	// updates nothing and cannot take the installation over.
	res, err := t.s.exec(ctx, `
		INSERT INTO github_app_installations (`+githubAppInstallationCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (installation_id) DO UPDATE SET
		    account_id = excluded.account_id, account_login = excluded.account_login,
		    account_type = excluded.account_type, suspended = excluded.suspended,
		    connected_by = excluded.connected_by, github_user_id = excluded.github_user_id,
		    updated_at = excluded.updated_at
		WHERE github_app_installations.team = excluded.team`,
		in.InstallationID, string(t.team), in.AccountID, in.AccountLogin, in.AccountType, suspended,
		in.ConnectedBy, in.GitHubUserID, at, at)
	if err != nil {
		return GitHubAppInstallation{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return GitHubAppInstallation{}, err
		}
		return GitHubAppInstallation{}, ErrInstallationBoundElsewhere
	}
	return t.GitHubAppInstallation(ctx, in.InstallationID)
}

// GitHubAppInstallation reads t's binding of installation, or ErrNotFound.
func (t *Tenant) GitHubAppInstallation(ctx context.Context, installation int64) (GitHubAppInstallation, error) {
	in, err := scanGitHubAppInstallation(t.s.queryRow(ctx, `SELECT `+githubAppInstallationCols+`
		FROM github_app_installations WHERE team = ? AND installation_id = ?`, string(t.team), installation))
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubAppInstallation{}, ErrNotFound
	}
	return in, err
}

// GitHubAppInstallations lists t's bindings, oldest first.
func (t *Tenant) GitHubAppInstallations(ctx context.Context) (_ []GitHubAppInstallation, err error) {
	rows, err := t.s.query(ctx, `SELECT `+githubAppInstallationCols+`
		FROM github_app_installations WHERE team = ? ORDER BY created_at, installation_id`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []GitHubAppInstallation
	for rows.Next() {
		in, err := scanGitHubAppInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// UnbindGitHubAppInstallation removes t's binding of installation and the
// subscriptions that relied on it. An installation t does not hold is
// ErrNotFound.
func (t *Tenant) UnbindGitHubAppInstallation(ctx context.Context, installation int64) error {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackOrLog(tx)
	res, err := tx.ExecContext(ctx, `DELETE FROM github_app_installations WHERE team = ? AND installation_id = ?`,
		string(t.team), installation)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM github_app_triggers WHERE team = ? AND installation_id = ?`,
		string(t.team), installation); err != nil {
		return err
	}
	return tx.Commit()
}

// GitHubAppInstallationTeam answers the team installation is bound to, or
// ErrNotFound. A webhook delivery and a repository's installation name no
// team, so this read is how either finds one.
func (o *Operator) GitHubAppInstallationTeam(ctx context.Context, installation int64) (GitHubAppInstallation, error) {
	in, err := scanGitHubAppInstallation(o.s.queryRow(ctx, `SELECT `+githubAppInstallationCols+`
		FROM github_app_installations WHERE installation_id = ?`, installation))
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubAppInstallation{}, ErrNotFound
	}
	return in, err
}

// UnbindGitHubAppInstallation removes installation's binding from whichever
// team holds it, with that team's subscriptions to it, and reports the team.
// GitHub's uninstall notice and the operator's move both name only the
// installation.
func (o *Operator) UnbindGitHubAppInstallation(ctx context.Context, installation int64) (Team, error) {
	in, err := o.GitHubAppInstallationTeam(ctx, installation)
	if err != nil {
		return "", err
	}
	t, err := o.s.ForTeam(ctx, in.Team)
	if err != nil {
		return "", err
	}
	return in.Team, t.UnbindGitHubAppInstallation(ctx, installation)
}

// SetGitHubAppInstallationSuspended records GitHub's suspension of
// installation. An unbound installation is ErrNotFound.
func (o *Operator) SetGitHubAppInstallationSuspended(ctx context.Context, installation int64, suspended bool, now time.Time) error {
	in, err := o.GitHubAppInstallationTeam(ctx, installation)
	if err != nil {
		return err
	}
	flag := 0
	if suspended {
		flag = 1
	}
	_, err = o.s.exec(ctx, `UPDATE github_app_installations SET suspended = ?, updated_at = ?
		WHERE team = ? AND installation_id = ?`, flag, now.UTC().Unix(), string(in.Team), installation)
	return err
}

const githubAppTriggerCols = `repository_id, pipeline, repository, installation_id, on_push, on_pull_request,
       fork_pull_requests, created_by, created_at`

func (t *Tenant) scanGitHubAppTriggers(rows *sql.Rows) ([]GitHubAppTrigger, error) {
	var out []GitHubAppTrigger
	for rows.Next() {
		tr := GitHubAppTrigger{Team: t.team}
		var push, pr, forks int
		var created int64
		if err := rows.Scan(&tr.RepositoryID, &tr.Pipeline, &tr.Repository, &tr.InstallationID, &push, &pr,
			&forks, &tr.CreatedBy, &created); err != nil {
			return nil, err
		}
		tr.Push, tr.PullRequest, tr.ForkPullRequests = push != 0, pr != 0, forks != 0
		tr.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, tr)
	}
	return out, rows.Err()
}

// PutGitHubAppTrigger writes tr for t, replacing the subscription of the
// same repository and pipeline. The installation must be one t holds.
func (t *Tenant) PutGitHubAppTrigger(ctx context.Context, tr GitHubAppTrigger, now time.Time) (GitHubAppTrigger, error) {
	repo, ok := ParseGitHubRepo(tr.Repository)
	if !ok || tr.RepositoryID <= 0 || strings.TrimSpace(tr.Pipeline) == "" || (!tr.Push && !tr.PullRequest) {
		return GitHubAppTrigger{}, fmt.Errorf("%w: a subscription needs a repository, a pipeline and at least one event", ErrInvalidInput)
	}
	if _, err := t.GitHubAppInstallation(ctx, tr.InstallationID); err != nil {
		return GitHubAppTrigger{}, err
	}
	tr.Team, tr.Repository, tr.CreatedAt = t.team, repo.Slug(), now.UTC().Truncate(time.Second)
	flag := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	_, err := t.s.exec(ctx, `
		INSERT INTO github_app_triggers (team, `+githubAppTriggerCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (team, repository_id, pipeline) DO UPDATE SET
		    repository = excluded.repository, installation_id = excluded.installation_id,
		    on_push = excluded.on_push, on_pull_request = excluded.on_pull_request,
		    fork_pull_requests = excluded.fork_pull_requests, created_by = excluded.created_by,
		    created_at = excluded.created_at`,
		string(t.team), tr.RepositoryID, tr.Pipeline, tr.Repository, tr.InstallationID, flag(tr.Push),
		flag(tr.PullRequest), flag(tr.ForkPullRequests), tr.CreatedBy, tr.CreatedAt.Unix())
	if err != nil {
		return GitHubAppTrigger{}, err
	}
	return tr, nil
}

// DeleteGitHubAppTrigger removes t's subscription of pipeline to repository,
// or answers ErrNotFound.
func (t *Tenant) DeleteGitHubAppTrigger(ctx context.Context, repositoryID int64, pipeline string) error {
	res, err := t.s.exec(ctx, `DELETE FROM github_app_triggers WHERE team = ? AND repository_id = ? AND pipeline = ?`,
		string(t.team), repositoryID, pipeline)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	return nil
}

// GitHubAppTriggers lists t's subscriptions by repository then pipeline.
func (t *Tenant) GitHubAppTriggers(ctx context.Context) (_ []GitHubAppTrigger, err error) {
	rows, err := t.s.query(ctx, `SELECT `+githubAppTriggerCols+` FROM github_app_triggers
		WHERE team = ? ORDER BY repository, pipeline`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return t.scanGitHubAppTriggers(rows)
}

// GitHubAppTriggersFor lists t's subscriptions to repositoryID through
// installation.
func (t *Tenant) GitHubAppTriggersFor(ctx context.Context, installation, repositoryID int64) (_ []GitHubAppTrigger, err error) {
	rows, err := t.s.query(ctx, `SELECT `+githubAppTriggerCols+` FROM github_app_triggers
		WHERE team = ? AND installation_id = ? AND repository_id = ? ORDER BY pipeline`,
		string(t.team), installation, repositoryID)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return t.scanGitHubAppTriggers(rows)
}

// RunUntrusted reports whether t's run runID was started by code nobody in
// the team wrote. A run t does not own is ErrNotFound.
func (t *Tenant) RunUntrusted(ctx context.Context, runID string) (bool, error) {
	var flag int
	err := t.s.queryRow(ctx, `SELECT untrusted FROM triggers WHERE team = ? AND id = ?`,
		string(t.team), runID).Scan(&flag)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return flag != 0, err
}

// ErrUntrustedRun is returned when a metered runner or a GitHub Actions
// credential reaches for a run of code nobody in its team wrote.
var ErrUntrustedRun = errors.New("store: this run is untrusted; only the team's own machines run it")

// safety: a metered claim is a cloud pod holding a pool credential, and fork
// code on it could spend the team's credits and reach its other runs, so the
// claim is refused even when it names the run directly.
func refuseUntrustedTx(ctx context.Context, tx *storeTx, team Team, runID string) error {
	var flag int
	err := tx.QueryRowContext(ctx, `SELECT untrusted FROM triggers WHERE team = ? AND id = ?`,
		string(team), runID).Scan(&flag)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if flag != 0 {
		return ErrUntrustedRun
	}
	return nil
}

// safety: the queue scans pass untrusted runs over for these claimants, so an
// untrusted run at the head of the queue cannot starve the work behind it.
const untrustedTriggerClause = ` AND triggers.untrusted = 0`

const untrustedNodeClause = ` AND NOT EXISTS (SELECT 1 FROM triggers ut WHERE ut.id = nodes.run_id AND ut.untrusted <> 0)`

func (s *Store) untrustedExcluded(ctx context.Context, claimant ClaimIdentity) (bool, error) {
	if _, scoped := GitHubRunnerScopeFrom(ctx); scoped {
		return true, nil
	}
	return s.TokenMetered(ctx, claimant.TokenPrefix)
}

// AccountIdentity returns the subject of accountID's identity at provider,
// such as the numeric GitHub user id, or ErrNotFound when the account has
// none linked.
func (s *Store) AccountIdentity(ctx context.Context, accountID, provider string) (string, error) {
	var subject string
	err := s.queryRow(ctx, `SELECT subject FROM identities WHERE account_id = ? AND provider = ?
		ORDER BY created_at LIMIT 1`, accountID, provider).Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return subject, err
}

// safety: a child or a retry of an untrusted run runs the same untrusted code,
// so the mark follows the parent and the source run whatever the caller set.
func inheritsUntrustedTx(ctx context.Context, tx *storeTx, team Team, t Trigger) (bool, error) {
	if t.Untrusted || (t.ParentRunID == "" && t.RetryOf == "") {
		return t.Untrusted, nil
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM triggers
		WHERE team = ? AND untrusted <> 0 AND id IN (?, ?)`,
		string(team), t.ParentRunID, t.RetryOf).Scan(&n)
	return n > 0, err
}
