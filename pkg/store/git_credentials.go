package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

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
}

// ErrClaimNotLive refuses a release whose claimant no longer holds a live
// claim on the run when the release commits, and a claim token whose claim
// was lost, finished, released or superseded by a later generation.
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
		ts := time.Unix(0, confirmed.Int64).UTC()
		c.ConfirmedAt = &ts
	}
	c.CreatedAt = time.Unix(0, created).UTC()
	c.UpdatedAt = time.Unix(0, updated).UTC()
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
	out, err := t.putGitCredentialTx(ctx, tx, c, now)
	if err != nil {
		return GitCredential{}, err
	}
	return out, tx.Commit()
}

func (t *Tenant) putGitCredentialTx(ctx context.Context, tx *storeTx, c GitCredential, now time.Time) (GitCredential, error) {
	if c.Host == "" || c.Secret == "" || (c.Kind != GitCredentialSSH && c.Kind != GitCredentialHTTPS) {
		return GitCredential{}, ErrInvalidInput
	}
	at := now.UTC().UnixNano()
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
		v := prev.ConfirmedAt.UnixNano()
		confirmed = &v
	}
	id, err := newIdentityID()
	if err != nil {
		return GitCredential{}, err
	}
	created := at
	if replacing {
		created = prev.CreatedAt.UnixNano()
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
	return out, nil
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
		now.UTC().UnixNano(), now.UTC().UnixNano(), string(t.team), c.ID, fingerprint); err != nil {
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

// ReleaseGitCredential reads the team's confirmed credential for rel.Host and
// records its release to rel.Runner, in one transaction, so a credential is
// never handed out without its audit row and a deleted one is never
// released. The same transaction admits the release under tok's claim as
// [Tenant.SpendSourceCredential] does, so a claim that lapses or moves after
// the caller's own check releases nothing. ErrNotFound when the team holds no
// confirmed credential for the host.
func (t *Tenant) ReleaseGitCredential(ctx context.Context, tok ClaimToken, rel GitCredentialRelease, now time.Time) (_ GitCredential, _ SourceSpend, err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return GitCredential{}, SourceSpend{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	spend, err := t.spendSourceClaimTx(ctx, tx, tok, now)
	if err != nil {
		return GitCredential{}, SourceSpend{}, err
	}
	c, err := scanGitCredential(tx.QueryRowContext(ctx, `
		SELECT `+gitCredentialCols+` FROM git_credentials
		WHERE team = ? AND host = ? AND confirmed_at IS NOT NULL`, string(t.team), rel.Host))
	if errors.Is(err, sql.ErrNoRows) {
		return GitCredential{}, SourceSpend{}, notFound("git credential", rel.Host)
	}
	if err != nil {
		return GitCredential{}, SourceSpend{}, err
	}
	rel.CredentialID, rel.Host = c.ID, c.Host
	if err := t.insertSourceReleaseTx(ctx, tx, tok, rel, now); err != nil {
		return GitCredential{}, SourceSpend{}, err
	}
	return c, spend, tx.Commit()
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

// SetGitCredentialMachine opts the team's machine token prefix in to, or out
// of, receiving the team's git credentials. Without it only a cloud runner
// receives them. ErrNotFound when prefix is no unrevoked machine token of
// the team.
func (t *Tenant) SetGitCredentialMachine(ctx context.Context, prefix string, enabled bool, by string, now time.Time) error {
	tok, err := t.TeamToken(ctx, prefix)
	if err != nil {
		return err
	}
	if tok.Family() != TokenFamilyMachine || (tok.RevokedAt != nil && !now.Before(*tok.RevokedAt)) {
		return notFound("machine token", prefix)
	}
	if !enabled {
		_, err := t.s.exec(ctx, `DELETE FROM git_credential_machines WHERE team = ? AND token_prefix = ?`,
			string(t.team), prefix)
		return err
	}
	_, err = t.s.exec(ctx, `
		INSERT INTO git_credential_machines (team, token_prefix, enabled_by, enabled_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (team, token_prefix) DO NOTHING`, string(t.team), prefix, by, now.UTC().UnixNano())
	return err
}

// GitCredentialMachines lists the team's machine token prefixes opted in to
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
