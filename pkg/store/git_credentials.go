package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A team git credential is a write-only secret bound to one host: an SSH
// deploy key with the host key its owner confirmed, or an HTTPS token. The
// controller releases it only at fetch time, to a runner holding a live claim
// on a run of the team whose source is on that host, and records every
// release.
const gitCredentialsTableSQLite = `CREATE TABLE IF NOT EXISTS git_credentials (
    id           TEXT PRIMARY KEY,
    team         TEXT NOT NULL,
    host         TEXT NOT NULL,
    kind         TEXT NOT NULL,
    username     TEXT NOT NULL DEFAULT '',
    secret       TEXT NOT NULL,
    known_hosts  TEXT NOT NULL DEFAULT '',
    fingerprint  TEXT NOT NULL DEFAULT '',
    confirmed_at INTEGER,
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_git_credentials_team_host ON git_credentials(team, host);
CREATE TABLE IF NOT EXISTS git_credential_releases (
    id            TEXT PRIMARY KEY,
    team          TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    host          TEXT NOT NULL,
    run_id        TEXT NOT NULL,
    runner        TEXT NOT NULL,
    token_prefix  TEXT NOT NULL,
    released_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_git_credential_releases_team ON git_credential_releases(team, released_at);
CREATE TABLE IF NOT EXISTS git_credential_machines (
    team         TEXT NOT NULL,
    token_prefix TEXT NOT NULL,
    enabled_by   TEXT NOT NULL DEFAULT '',
    enabled_at   INTEGER NOT NULL,
    PRIMARY KEY (team, token_prefix)
);`

var gitCredentialsTablePostgres = strings.NewReplacer("INTEGER", "BIGINT").Replace(gitCredentialsTableSQLite)

func applyGitCredentialsMigration(ctx context.Context, tx *storeTx, ddl string) error {
	for _, stmt := range splitStatements(ddl) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// Kinds of [GitCredential].
const (
	GitCredentialSSH   = "ssh"
	GitCredentialHTTPS = "https"
)

// MaxGitCredentialsPerTeam bounds a team's stored git credentials, one per
// host.
const MaxGitCredentialsPerTeam = 50

// ErrGitCredentialLimit refuses a team's credential past
// [MaxGitCredentialsPerTeam].
var ErrGitCredentialLimit = fmt.Errorf("store: a team stores at most %d git credentials", MaxGitCredentialsPerTeam)

// ErrFingerprintMismatch refuses a confirmation that names a host key other
// than the one the credential pinned.
var ErrFingerprintMismatch = errors.New("store: the fingerprint is not the host key this credential pinned")

// GitCredential is one team's credential for one host. Secret is the value
// as stored, which the controller seals; nothing returns it to a caller but
// a release.
type GitCredential struct {
	ID       string
	Team     Team
	Host     string
	Kind     string
	Username string
	Secret   string
	// KnownHosts is the pinned known_hosts entry of an ssh credential, and
	// Fingerprint its SHA256 fingerprint.
	KnownHosts  string
	Fingerprint string
	// ConfirmedAt is when the owner confirmed the pinned host key; an ssh
	// credential is released only once it is set. An https credential is
	// confirmed when it is stored.
	ConfirmedAt *time.Time
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// GitCredentialRelease is the audit row one release writes.
type GitCredentialRelease struct {
	ID           string
	Team         Team
	CredentialID string
	Host         string
	RunID        string
	Runner       string
	TokenPrefix  string
	ReleasedAt   time.Time
	// Claimant is the claim the release is made against; the release
	// re-checks it inside its own transaction. Not stored.
	Claimant ClaimIdentity
}

// ErrClaimNotLive refuses a release whose claimant no longer holds a live
// claim on the run when the release commits.
var ErrClaimNotLive = errors.New("store: the claim on the run is no longer live")

const gitCredentialCols = `id, team, host, kind, username, secret, known_hosts, fingerprint,
	confirmed_at, created_by, created_at, updated_at`

func scanGitCredential(row interface{ Scan(...any) error }) (GitCredential, error) {
	var c GitCredential
	var team string
	var confirmed sql.NullInt64
	var created, updated int64
	if err := row.Scan(&c.ID, &team, &c.Host, &c.Kind, &c.Username, &c.Secret, &c.KnownHosts, &c.Fingerprint,
		&confirmed, &c.CreatedBy, &created, &updated); err != nil {
		return GitCredential{}, err
	}
	c.Team = Team(team)
	if confirmed.Valid {
		ts := time.Unix(confirmed.Int64, 0).UTC()
		c.ConfirmedAt = &ts
	}
	c.CreatedAt = time.Unix(created, 0).UTC()
	c.UpdatedAt = time.Unix(updated, 0).UTC()
	return c, nil
}

// PutGitCredential stores c for its host, replacing any credential the team
// already holds there. An https credential is confirmed at once. A
// replacement ssh credential stays confirmed only when it pins the same
// fingerprint the owner confirmed before; any other ssh credential waits for
// [Tenant.ConfirmGitCredential].
func (t *Tenant) PutGitCredential(ctx context.Context, c GitCredential, now time.Time) (_ GitCredential, err error) {
	if c.Host == "" || c.Secret == "" || (c.Kind != GitCredentialSSH && c.Kind != GitCredentialHTTPS) {
		return GitCredential{}, fmt.Errorf("%w: a git credential needs a host, a kind and a secret", ErrInvalidInput)
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return GitCredential{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := t.lockTeamTx(ctx, tx); err != nil {
		return GitCredential{}, err
	}
	at := now.UTC().Unix()
	prev, err := scanGitCredential(tx.QueryRowContext(ctx,
		`SELECT `+gitCredentialCols+` FROM git_credentials WHERE team = ? AND host = ?`, string(t.team), c.Host))
	replacing := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GitCredential{}, err
	}
	if !replacing {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM git_credentials WHERE team = ?`,
			string(t.team)).Scan(&n); err != nil {
			return GitCredential{}, err
		}
		if n >= MaxGitCredentialsPerTeam {
			return GitCredential{}, ErrGitCredentialLimit
		}
	}
	var confirmed *int64
	switch {
	case c.Kind == GitCredentialHTTPS:
		confirmed = &at
	case replacing && prev.Kind == GitCredentialSSH && prev.ConfirmedAt != nil && prev.Fingerprint == c.Fingerprint:
		v := prev.ConfirmedAt.Unix()
		confirmed = &v
	}
	id, err := newIdentityID()
	if err != nil {
		return GitCredential{}, err
	}
	created := at
	if replacing {
		created = prev.CreatedAt.Unix()
		if _, err := tx.ExecContext(ctx, `DELETE FROM git_credentials WHERE team = ? AND host = ?`,
			string(t.team), c.Host); err != nil {
			return GitCredential{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO git_credentials (`+gitCredentialCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(t.team), c.Host, c.Kind, c.Username, c.Secret, c.KnownHosts, c.Fingerprint,
		confirmed, c.CreatedBy, created, at); err != nil {
		return GitCredential{}, err
	}
	out, err := scanGitCredential(tx.QueryRowContext(ctx,
		`SELECT `+gitCredentialCols+` FROM git_credentials WHERE team = ? AND id = ?`, string(t.team), id))
	if err != nil {
		return GitCredential{}, err
	}
	return out, tx.Commit()
}

// ConfirmGitCredential marks the team's ssh credential for host usable,
// once fingerprint is the one it pinned. A credential that is already
// confirmed stays as it is.
func (t *Tenant) ConfirmGitCredential(ctx context.Context, host, fingerprint string, now time.Time) (GitCredential, error) {
	c, err := t.GitCredentialForHost(ctx, host)
	if err != nil {
		return GitCredential{}, err
	}
	if c.Kind != GitCredentialSSH || c.Fingerprint == "" || c.Fingerprint != fingerprint {
		return GitCredential{}, ErrFingerprintMismatch
	}
	if _, err := t.s.exec(ctx, `
		UPDATE git_credentials SET confirmed_at = ?, updated_at = ?
		WHERE team = ? AND id = ? AND fingerprint = ? AND confirmed_at IS NULL`,
		now.UTC().Unix(), now.UTC().Unix(), string(t.team), c.ID, fingerprint); err != nil {
		return GitCredential{}, err
	}
	return t.GitCredentialForHost(ctx, host)
}

// GitCredentialForHost reads the team's credential for host, or ErrNotFound.
func (t *Tenant) GitCredentialForHost(ctx context.Context, host string) (GitCredential, error) {
	c, err := scanGitCredential(t.s.queryRow(ctx,
		`SELECT `+gitCredentialCols+` FROM git_credentials WHERE team = ? AND host = ?`, string(t.team), host))
	if errors.Is(err, sql.ErrNoRows) {
		return GitCredential{}, notFound("git credential", host)
	}
	return c, err
}

// GitCredentials lists the team's credentials by host.
func (t *Tenant) GitCredentials(ctx context.Context) (_ []GitCredential, err error) {
	rows, err := t.s.query(ctx,
		`SELECT `+gitCredentialCols+` FROM git_credentials WHERE team = ? ORDER BY host`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []GitCredential
	for rows.Next() {
		c, err := scanGitCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteGitCredential removes the team's credential for host, so no later
// release finds it. ErrNotFound when the team holds none there.
func (t *Tenant) DeleteGitCredential(ctx context.Context, host string) error {
	res, err := t.s.exec(ctx, `DELETE FROM git_credentials WHERE team = ? AND host = ?`, string(t.team), host)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return notFound("git credential", host)
	}
	return nil
}

// ReleaseGitCredential reads the team's confirmed credential for host and
// records its release to the runner rel names, in one transaction, so a
// credential is never handed out without its audit row and a deleted one is
// never released. The same transaction re-checks, and on Postgres locks,
// rel.Claimant's live claim on rel.RunID in the team, so a claim that lapses
// or moves after the caller's own check releases nothing. ErrNotFound when
// the team holds no confirmed credential for host; ErrClaimNotLive when the
// claim is gone.
func (t *Tenant) ReleaseGitCredential(ctx context.Context, host string, rel GitCredentialRelease, now time.Time) (_ GitCredential, err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return GitCredential{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := t.lockLiveRunClaimTx(ctx, tx, rel.RunID, rel.Claimant, now); err != nil {
		return GitCredential{}, err
	}
	c, err := scanGitCredential(tx.QueryRowContext(ctx, `
		SELECT `+gitCredentialCols+` FROM git_credentials
		WHERE team = ? AND host = ? AND confirmed_at IS NOT NULL`, string(t.team), host))
	if errors.Is(err, sql.ErrNoRows) {
		return GitCredential{}, notFound("git credential", host)
	}
	if err != nil {
		return GitCredential{}, err
	}
	id, err := newIdentityID()
	if err != nil {
		return GitCredential{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO git_credential_releases (id, team, credential_id, host, run_id, runner, token_prefix, released_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(t.team), c.ID, c.Host, rel.RunID, rel.Runner, rel.TokenPrefix, now.UTC().Unix()); err != nil {
		return GitCredential{}, err
	}
	return c, tx.Commit()
}

// lockLiveRunClaimTx proves claimant holds a live claim on one of runID's
// nodes or on its trigger, in t's team, and holds that row's lock until tx
// ends, so the claim cannot be renewed away or reassigned under the release.
func (t *Tenant) lockLiveRunClaimTx(ctx context.Context, tx *storeTx, runID string, claimant ClaimIdentity, now time.Time) error {
	if !claimant.bound() || runID == "" {
		return ErrClaimNotLive
	}
	at := now.UnixNano()
	var one int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM nodes
		WHERE team = ? AND run_id = ? AND claim_principal = ? AND claim_token_prefix = ?
		  AND `+nodeClaimLiveSQL("")+` LIMIT 1`+tx.forUpdate(),
		string(t.team), runID, claimant.Principal, claimant.TokenPrefix, at).Scan(&one)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = tx.QueryRowContext(ctx, `
		SELECT 1 FROM triggers
		WHERE team = ? AND id = ? AND claim_principal = ? AND claim_token_prefix = ?
		  AND `+triggerClaimLiveSQL("")+tx.forUpdate(),
		string(t.team), runID, claimant.Principal, claimant.TokenPrefix, at).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClaimNotLive
	}
	return err
}

// GitCredentialReleases lists the team's most recent releases, newest
// first, at most limit of them.
func (t *Tenant) GitCredentialReleases(ctx context.Context, limit int) (_ []GitCredentialRelease, err error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := t.s.query(ctx, `
		SELECT id, team, credential_id, host, run_id, runner, token_prefix, released_at
		FROM git_credential_releases WHERE team = ?
		ORDER BY released_at DESC, id DESC LIMIT ?`, string(t.team), limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []GitCredentialRelease
	for rows.Next() {
		var r GitCredentialRelease
		var team string
		var at int64
		if err := rows.Scan(&r.ID, &team, &r.CredentialID, &r.Host, &r.RunID, &r.Runner, &r.TokenPrefix, &at); err != nil {
			return nil, err
		}
		r.Team = Team(team)
		r.ReleasedAt = time.Unix(at, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetGitCredentialMachine opts the team's runner token prefix in to, or out
// of, receiving the team's git credentials. Without it only a cloud runner
// receives them. ErrNotFound when prefix is no live runner token of the
// team.
func (t *Tenant) SetGitCredentialMachine(ctx context.Context, prefix string, enabled bool, by string, now time.Time) error {
	tok, err := t.RunnerToken(ctx, prefix)
	if err != nil {
		return err
	}
	if tok.RevokedAt != nil && !now.Before(*tok.RevokedAt) {
		return notFound("runner token", prefix)
	}
	if !enabled {
		_, err := t.s.exec(ctx, `DELETE FROM git_credential_machines WHERE team = ? AND token_prefix = ?`,
			string(t.team), prefix)
		return err
	}
	_, err = t.s.exec(ctx, `
		INSERT INTO git_credential_machines (team, token_prefix, enabled_by, enabled_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (team, token_prefix) DO NOTHING`, string(t.team), prefix, by, now.UTC().Unix())
	return err
}

// GitCredentialMachines lists the team's runner token prefixes opted in to
// receiving its git credentials.
func (t *Tenant) GitCredentialMachines(ctx context.Context) (_ map[string]bool, err error) {
	rows, err := t.s.query(ctx, `SELECT token_prefix FROM git_credential_machines WHERE team = ?`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := map[string]bool{}
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return nil, err
		}
		out[prefix] = true
	}
	return out, rows.Err()
}

// GitCredentialMachine reports whether the team's owner opted the runner
// token prefix in to receiving its git credentials.
func (t *Tenant) GitCredentialMachine(ctx context.Context, prefix string) (bool, error) {
	var n int
	err := t.s.queryRow(ctx, `SELECT COUNT(*) FROM git_credential_machines WHERE team = ? AND token_prefix = ?`,
		string(t.team), prefix).Scan(&n)
	return n > 0, err
}

// RotateGitCredentialSecrets stores what reseal returns for every team's
// git credential secret, in one transaction, so a key rotation moves them
// with the secrets table.
func (s *Store) RotateGitCredentialSecrets(ctx context.Context, reseal func(GitCredential) (string, error)) (rotated int, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackUnlessDone(tx, &err)
	// safety: SQLite serves one connection, so every row is read before the
	// first write on this transaction.
	current, err := selectGitCredentialsTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, c := range current {
		value, rerr := reseal(c)
		if rerr != nil {
			return 0, rerr
		}
		if _, err := tx.ExecContext(ctx, `UPDATE git_credentials SET secret = ? WHERE team = ? AND id = ?`,
			value, string(c.Team), c.ID); err != nil {
			return 0, err
		}
	}
	return len(current), tx.Commit()
}

func selectGitCredentialsTx(ctx context.Context, tx *storeTx) (_ []GitCredential, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+gitCredentialCols+` FROM git_credentials ORDER BY team, host`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []GitCredential
	for rows.Next() {
		c, err := scanGitCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
