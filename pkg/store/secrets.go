package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Secret is one row in the secrets table. Masked controls log
// redaction; defaults to true. Repo is the owning repository slug, or
// "" for an unscoped secret every run can resolve.
type Secret struct {
	Name      string
	Value     string
	Principal string
	Repo      string
	Masked    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateOrReplaceSecret upserts a row; created_at is preserved. repo
// scopes the secret to one repository slug; "" stores it unscoped.
func (s *Store) CreateOrReplaceSecret(name, value, principal, repo string, masked bool, now time.Time) error {
	if name == "" {
		return errors.New("secrets: name required")
	}
	ts := now.UTC().Unix()
	maskedInt := 0
	if masked {
		maskedInt = 1
	}
	_, err := s.execNoCtx(`
        INSERT INTO secrets (name, value, principal, masked, repo, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(name, repo) DO UPDATE SET
            value = excluded.value,
            principal = excluded.principal,
            masked = excluded.masked,
            updated_at = excluded.updated_at
    `, name, value, principal, maskedInt, repo, ts, ts)
	return err
}

// GetSecret returns the unscoped row including Value.
func (s *Store) GetSecret(name string) (*Secret, error) {
	return s.GetSecretForRepo(name, "")
}

// GetSecretForRepo returns the row named for repo, falling back to the
// unscoped row when the repository has none of its own. ErrNotFound
// when neither exists.
func (s *Store) GetSecretForRepo(name, repo string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	if repo != "" {
		sec, err := s.readSecret(name, repo)
		if err == nil || !errors.Is(err, ErrNotFound) {
			return sec, err
		}
	}
	return s.readSecret(name, "")
}

func (s *Store) readSecret(name, repo string) (*Secret, error) {
	row := s.queryRowNoCtx(`
        SELECT name, value, principal, repo, masked, created_at, updated_at
          FROM secrets
         WHERE name = ? AND repo = ?
    `, name, repo)
	var sec Secret
	var maskedInt int
	var created, updated int64
	err := row.Scan(&sec.Name, &sec.Value, &sec.Principal, &sec.Repo, &maskedInt, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sec.Masked = maskedInt != 0
	sec.CreatedAt = time.Unix(created, 0).UTC()
	sec.UpdatedAt = time.Unix(updated, 0).UTC()
	return &sec, nil
}

// ListSecrets returns rows ordered by name then repo. HTTP handlers
// must blank Value before serializing.
func (s *Store) ListSecrets() ([]Secret, error) {
	rows, err := s.queryNoCtx(`
        SELECT name, value, principal, repo, masked, created_at, updated_at
          FROM secrets
         ORDER BY name, repo
    `)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Secret
	for rows.Next() {
		var sec Secret
		var maskedInt int
		var created, updated int64
		if err := rows.Scan(&sec.Name, &sec.Value, &sec.Principal, &sec.Repo, &maskedInt, &created, &updated); err != nil {
			return nil, err
		}
		sec.Masked = maskedInt != 0
		sec.CreatedAt = time.Unix(created, 0).UTC()
		sec.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, sec)
	}
	return out, nil
}

// DeleteSecret removes the row owned by repo ("" for the unscoped
// row); ErrNotFound when missing.
func (s *Store) DeleteSecret(name, repo string) error {
	res, err := s.execNoCtx(`DELETE FROM secrets WHERE name = ? AND repo = ?`, name, repo)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RepoForPrincipalClaim returns the repository slug of a run in which
// claimant holds an unexpired node claim, or "" when it holds none.
// The longest-lived claim wins when a claimant holds several.
func (s *Store) RepoForPrincipalClaim(ctx context.Context, claimant ClaimIdentity, now time.Time) (string, error) {
	if !claimant.bound() {
		return "", nil
	}
	var repo string
	err := s.queryRow(ctx, `
        SELECT runs.repo
          FROM nodes JOIN runs ON runs.id = nodes.run_id
         WHERE nodes.claim_principal = ? AND nodes.claim_token_prefix = ?
           AND `+nodeClaimLiveSQL+`
         ORDER BY nodes.lease_expires_at DESC
         LIMIT 1`, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&repo)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return repo, nil
}
