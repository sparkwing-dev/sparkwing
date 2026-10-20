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
