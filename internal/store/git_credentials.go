package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Kinds of [GitCredential].
const (
	GitCredentialSSH   = "ssh"
	GitCredentialHTTPS = "https"
)

// maxGitCredentialsPerTeam bounds repository credentials and explicit host-wide grants.
const maxGitCredentialsPerTeam = 50

// ErrGitCredentialLimit refuses a team's credential past
// [maxGitCredentialsPerTeam].
var ErrGitCredentialLimit = fmt.Errorf("store: a team stores at most %d git credentials", maxGitCredentialsPerTeam)

// ErrFingerprintMismatch refuses a confirmation that names a host key other
// than the one the credential pinned.
var ErrFingerprintMismatch = errors.New("store: the fingerprint is not the host key this credential pinned")

// GitCredential is a sealed repository credential or an explicit host-wide grant.
// Only an authorized release returns its Secret.
type GitCredential struct {
	Repository string
	HostWide   bool
	ID         string
	Team       Team
	Host       string
	Kind       string
	Username   string
	Secret     string
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

const gitCredentialCols = `id, team, host, repository_key, host_wide, kind, username, secret, known_hosts, fingerprint,
	confirmed_at, created_by, created_at, updated_at`

func scanGitCredential(row interface{ Scan(...any) error }) (GitCredential, error) {
	var c GitCredential
	var team string
	var confirmed sql.NullInt64
	var created, updated int64
	if err := row.Scan(&c.ID, &team, &c.Host, &c.Repository, &c.HostWide, &c.Kind, &c.Username, &c.Secret, &c.KnownHosts, &c.Fingerprint,
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
	if c.HostWide == (c.Repository != "") {
		return GitCredential{}, ErrInvalidInput
	}
	if c.Repository != "" {
		if err := runspec.ValidateRepoUse(runspec.RepoKey(c.Repository), runspec.RepoMachineStore); err != nil {
			return GitCredential{}, ErrInvalidInput
		}
	}
	if c.Host == "" || c.Secret == "" || (c.Kind != GitCredentialSSH && c.Kind != GitCredentialHTTPS) {
		return GitCredential{}, ErrInvalidInput
	}
	at := now.UTC().UnixNano()
	prev, err := scanGitCredential(tx.QueryRowContext(ctx,
		`SELECT `+gitCredentialCols+` FROM git_credentials WHERE team = ? AND host = ? AND repository_key = ?`, string(t.team), c.Host, c.Repository))
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
		if n >= maxGitCredentialsPerTeam {
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
		if _, err := tx.execContext(ctx, `DELETE FROM git_credentials WHERE team = ? AND host = ? AND repository_key = ?`,
			string(t.team), c.Host, c.Repository); err != nil {
			return GitCredential{}, err
		}
	}
	if _, err := tx.execContext(ctx, `
		INSERT INTO git_credentials (`+gitCredentialCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(t.team), c.Host, c.Repository, c.HostWide, c.Kind, c.Username, c.Secret, c.KnownHosts, c.Fingerprint,
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

// GitCredentialForRepository reads the exact repository grant, falling back to an explicit host-wide grant.
func (t *Tenant) GitCredentialForRepository(ctx context.Context, repository, host string) (GitCredential, error) {
	c, err := scanGitCredential(t.s.queryRow(ctx,
		`SELECT `+gitCredentialCols+` FROM git_credentials WHERE team = ? AND host = ? AND (repository_key = ? OR host_wide = 1) ORDER BY host_wide LIMIT 1`, string(t.team), host, repository))
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

// CredentialScope binds the ciphertext to its repository or explicit host-wide grant.
func (c GitCredential) CredentialScope() string {
	if c.HostWide {
		return "git-credential:host:" + c.Host
	}
	return "git-credential:repo:" + c.Repository
}
