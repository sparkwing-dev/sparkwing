package store

import (
	"context"
	"errors"
	"time"
)

// Secret is one row in the secrets table. Masked controls log
// redaction; defaults to true. Repo and Pipeline name the one repository and
// pipeline that own the row, or are both "" for an unscoped secret. Repo is
// the canonical repository key and RepoName its display at write time. Shared
// marks an unscoped secret a run may resolve; an unscoped row that is not
// shared answers admin only.
//
// A run reads the secrets of the repository and pipeline its sealed
// provenance speaks for: its own, or its parent's when it is a child at the
// parent's exact revision. A child that left that revision reads only shared
// secrets. Two repositories with a pipeline of the same name never share a
// pipeline secret, matching GitHub repository secrets.
type Secret struct {
	Ephemeral bool `json:"-"`
	Name      string
	Value     string
	Principal string
	Repo      string
	RepoName  string
	Pipeline  string
	Masked    bool
	Shared    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// A signed-up team's secrets are rows the operator stores for free, so they
// are bounded in count and size; the operator's own team is not.
const (
	// maxSecretsPerTeam bounds the secret rows one signed-up team holds.
	maxSecretsPerTeam = 100
	// maxSecretValueBytes bounds one stored secret value of a signed-up
	// team, as stored, sealed envelope included.
	maxSecretValueBytes = 128 << 10
)

// ErrSecretLimit refuses a signed-up team's secret past [maxSecretsPerTeam]
// or [maxSecretValueBytes].
var ErrSecretLimit = errors.New("store: secret limit reached")

// SealScope is the row scope a sealed value is bound to, so a value cannot be
// moved to another repository's or pipeline's row. A repository key never
// contains '#'.
func (s Secret) SealScope() string {
	if s.Repo == "" {
		return s.Pipeline
	}
	return s.Repo + "#" + s.Pipeline
}

const secretCols = "name,value,principal,repo,repo_name,pipeline,masked,shared,created_at,updated_at"

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetSecretRow returns t's row stored under exactly this name, repository
// and pipeline, and ErrNotFound when there is none.
func (t *Tenant) GetSecretRow(name, repo, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	rows, err := t.s.queryNoCtx(`SELECT `+secretCols+` FROM secrets WHERE team = ? AND name = ? AND repo = ? AND pipeline = ?`, string(t.team), name, repo, pipeline)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out, err := scanSecretRows(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, notFound("secret", name)
	}
	return &out[0], nil
}

// ListSecrets returns t's rows ordered by name, repository and pipeline. HTTP
// handlers must blank Value before serializing.
func (t *Tenant) ListSecrets() ([]Secret, error) {
	rows, err := t.s.queryNoCtx(`SELECT `+secretCols+` FROM secrets WHERE team = ? ORDER BY name, repo, pipeline`, string(t.team))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanSecretRows(rows)
}

// TeamSecret is a secret row together with the team that owns it, for the
// few readers that walk every team at once.
type TeamSecret struct {
	Team   Team
	Secret Secret
}

// SampleSealedSecrets returns up to limit rows, from any team, whose stored
// value begins with sealedPrefix. A controller opens them to prove its key
// is the one the table was sealed under before it seals anything more.
func (o *Operator) SampleSealedSecrets(ctx context.Context, sealedPrefix string, limit int) (_ []TeamSecret, err error) {
	if sealedPrefix == "" || limit < 1 {
		return nil, errors.New("secrets: sample needs a prefix and a positive limit")
	}
	rows, err := o.s.query(ctx, `SELECT `+secretCols+`,team FROM secrets
         WHERE substr(value, 1, ?) = ?
         ORDER BY updated_at DESC, team, name, repo, pipeline
         LIMIT ?`, len(sealedPrefix), sealedPrefix, limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return scanTeamSecretRows(rows)
}

func scanTeamSecretRows(rows rowCursor) ([]TeamSecret, error) {
	var out []TeamSecret
	for rows.Next() {
		var row TeamSecret
		if err := scanSecret(rows, &row.Secret, &row.Team); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanSecretRows(rows rowCursor) ([]Secret, error) {
	var out []Secret
	for rows.Next() {
		var sec Secret
		if err := scanSecret(rows, &sec); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// scanSecret reads one row selected as [secretCols] followed by extra columns.
func scanSecret(rows rowCursor, sec *Secret, extra ...any) error {
	var masked, shared int
	var created, updated int64
	dest := []any{&sec.Name, &sec.Value, &sec.Principal, &sec.Repo, &sec.RepoName, &sec.Pipeline, &masked, &shared, &created, &updated}
	if err := rows.Scan(append(dest, extra...)...); err != nil {
		return err
	}
	sec.Masked, sec.Shared = masked != 0, shared != 0
	sec.CreatedAt, sec.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return nil
}
