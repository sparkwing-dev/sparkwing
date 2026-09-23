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
	CreatedBy      string
	CreatedAt      time.Time
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
    created_by         TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    PRIMARY KEY (team, repository_id, pipeline)
);
CREATE INDEX IF NOT EXISTS idx_github_app_triggers_installation ON github_app_triggers(installation_id, repository_id);
CREATE TABLE IF NOT EXISTS github_app_connect_states (
    nonce      TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS github_app_deliveries (
    digest      TEXT PRIMARY KEY,
    delivery_id TEXT NOT NULL,
    received_at INTEGER NOT NULL
)
`

var githubAppTablesPostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(githubAppTablesSQLite)

func applyGitHubAppMigrationSQLite(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(githubAppTablesSQLite) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func applyGitHubAppMigrationPostgres(ctx context.Context, tx *storeTx) error {
	for _, stmt := range splitStatements(githubAppTablesPostgres) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
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
       created_by, created_at`

func (t *Tenant) scanGitHubAppTriggers(rows *sql.Rows) ([]GitHubAppTrigger, error) {
	var out []GitHubAppTrigger
	for rows.Next() {
		tr := GitHubAppTrigger{Team: t.team}
		var push, pr int
		var created int64
		if err := rows.Scan(&tr.RepositoryID, &tr.Pipeline, &tr.Repository, &tr.InstallationID, &push, &pr,
			&tr.CreatedBy, &created); err != nil {
			return nil, err
		}
		tr.Push, tr.PullRequest = push != 0, pr != 0
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (team, repository_id, pipeline) DO UPDATE SET
		    repository = excluded.repository, installation_id = excluded.installation_id,
		    on_push = excluded.on_push, on_pull_request = excluded.on_pull_request,
		    created_by = excluded.created_by, created_at = excluded.created_at`,
		string(t.team), tr.RepositoryID, tr.Pipeline, tr.Repository, tr.InstallationID, flag(tr.Push),
		flag(tr.PullRequest), tr.CreatedBy, tr.CreatedAt.Unix())
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

// ConsumeGitHubAppConnectState records that the connect flow named by nonce
// finished, and reports false when one already did. The record lives until
// the state expires, so every replica and a restarted controller refuse a
// state that was used once.
func (s *Store) ConsumeGitHubAppConnectState(ctx context.Context, nonce string, expires, now time.Time) (bool, error) {
	if nonce == "" {
		return false, nil
	}
	if _, err := s.exec(ctx, `DELETE FROM github_app_connect_states WHERE expires_at <= ?`, now.Unix()); err != nil {
		return false, err
	}
	_, err := s.exec(ctx, `INSERT INTO github_app_connect_states (nonce, expires_at) VALUES (?, ?)`,
		nonce, expires.Unix())
	if isUniqueViolation(err) {
		return false, nil
	}
	return err == nil, err
}

// GitHubAppDeliveryRetention is how long a delivery's digest is remembered;
// a delivery replayed later than this is accepted again.
const GitHubAppDeliveryRetention = 90 * 24 * time.Hour

// GitHubAppDeliverySeen reports whether a delivery with digest was processed
// before, for any team.
func (s *Store) GitHubAppDeliverySeen(ctx context.Context, digest string) (bool, error) {
	var n int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM github_app_deliveries WHERE digest = ?`, digest).Scan(&n)
	return n > 0, err
}

// RecordGitHubAppDelivery remembers that a delivery with digest was
// processed, and forgets digests older than [GitHubAppDeliveryRetention].
func (s *Store) RecordGitHubAppDelivery(ctx context.Context, digest, delivery string, now time.Time) error {
	if _, err := s.exec(ctx, `DELETE FROM github_app_deliveries WHERE received_at <= ?`,
		now.Add(-GitHubAppDeliveryRetention).Unix()); err != nil {
		return err
	}
	_, err := s.exec(ctx, `INSERT INTO github_app_deliveries (digest, delivery_id, received_at) VALUES (?, ?, ?)
		ON CONFLICT (digest) DO NOTHING`, digest, delivery, now.Unix())
	return err
}
