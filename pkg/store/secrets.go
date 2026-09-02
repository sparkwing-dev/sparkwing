package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Secret is one row in the secrets table. Masked controls log
// redaction; defaults to true. Repo is the owning repository slug, or
// "" for an unscoped secret. Shared marks an unscoped secret a run may
// resolve; an unscoped row that is not shared answers admin only.
type Secret struct {
	Name      string
	Value     string
	Principal string
	Repo      string
	Masked    bool
	Shared    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateOrReplaceSecret upserts sec; created_at is preserved. Repo
// scopes the secret to one repository slug, and Shared opens an
// unscoped row to every run.
func (s *Store) CreateOrReplaceSecret(sec Secret, now time.Time) error {
	if sec.Name == "" {
		return errors.New("secrets: name required")
	}
	ts := now.UTC().Unix()
	_, err := s.execNoCtx(`
        INSERT INTO secrets (name, value, principal, masked, repo, shared, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(name, repo) DO UPDATE SET
            value = excluded.value,
            principal = excluded.principal,
            masked = excluded.masked,
            shared = excluded.shared,
            updated_at = excluded.updated_at
    `, sec.Name, sec.Value, sec.Principal, boolInt(sec.Masked), sec.Repo, boolInt(sec.Shared), ts, ts)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetSecret returns the unscoped row including Value.
func (s *Store) GetSecret(name string) (*Secret, error) {
	return s.readSecret(name, "")
}

// GetSecretRow returns the row stored under exactly this name and
// repo, and ErrNotFound when there is none. Unlike GetSecretForRepo it
// never falls back to the unscoped row.
func (s *Store) GetSecretRow(name, repo string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	return s.readSecret(name, repo)
}

// GetSecretForRepo returns the row named for repo, falling back to the
// unscoped row when the repository has none of its own. ErrNotFound
// when neither exists. This is the administrative read: it reaches an
// unscoped row whether or not it is shared.
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

// GetSecretForRun returns the row repo owns, falling back to an
// unscoped row only when that row is shared. ErrNotFound otherwise, so
// an unshared unscoped secret is indistinguishable from a missing one.
func (s *Store) GetSecretForRun(name, repo string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	if repo != "" {
		sec, err := s.readSecret(name, repo)
		if err == nil || !errors.Is(err, ErrNotFound) {
			return sec, err
		}
	}
	sec, err := s.readSecret(name, "")
	if err != nil {
		return nil, err
	}
	if !sec.Shared {
		return nil, ErrNotFound
	}
	return sec, nil
}

func (s *Store) readSecret(name, repo string) (*Secret, error) {
	row := s.queryRowNoCtx(`
        SELECT name, value, principal, repo, masked, shared, created_at, updated_at
          FROM secrets
         WHERE name = ? AND repo = ?
    `, name, repo)
	var sec Secret
	var maskedInt, sharedInt int
	var created, updated int64
	err := row.Scan(&sec.Name, &sec.Value, &sec.Principal, &sec.Repo, &maskedInt, &sharedInt, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sec.Masked = maskedInt != 0
	sec.Shared = sharedInt != 0
	sec.CreatedAt = time.Unix(created, 0).UTC()
	sec.UpdatedAt = time.Unix(updated, 0).UTC()
	return &sec, nil
}

// ListSecrets returns rows ordered by name then repo. HTTP handlers
// must blank Value before serializing.
func (s *Store) ListSecrets() ([]Secret, error) {
	rows, err := s.queryNoCtx(`
        SELECT name, value, principal, repo, masked, shared, created_at, updated_at
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
		var maskedInt, sharedInt int
		var created, updated int64
		if err := rows.Scan(&sec.Name, &sec.Value, &sec.Principal, &sec.Repo, &maskedInt, &sharedInt, &created, &updated); err != nil {
			return nil, err
		}
		sec.Masked = maskedInt != 0
		sec.Shared = sharedInt != 0
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

// RepoForClaimedRun returns the repository slug of runID when claimant
// holds live work on it: an unexpired claim on one of its nodes, or the
// unexpired claim on the trigger that created it. ErrNotFound when the
// claimant holds neither, so a caller cannot name a run it is not
// executing.
func (s *Store) RepoForClaimedRun(ctx context.Context, runID string, claimant ClaimIdentity, now time.Time) (string, error) {
	if !claimant.bound() || runID == "" {
		return "", ErrNotFound
	}
	var repo string
	err := s.queryRow(ctx, `
        SELECT runs.repo
          FROM runs
         WHERE runs.id = ?
           AND (EXISTS (SELECT 1 FROM nodes
                         WHERE nodes.run_id = runs.id
                           AND nodes.claim_principal = ? AND nodes.claim_token_prefix = ?
                           AND `+nodeClaimLiveSQL+`)
             OR EXISTS (SELECT 1 FROM triggers
                         WHERE triggers.id = runs.id
                           AND triggers.claim_principal = ? AND triggers.claim_token_prefix = ?
                           AND `+triggerClaimLiveSQL+`))`,
		runID,
		claimant.Principal, claimant.TokenPrefix, now.UnixNano(),
		claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&repo)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return repo, nil
}

// ReposForClaimant returns the distinct repository slugs of the runs
// claimant currently holds work in, through node claims or trigger
// claims. Empty when it holds none.
func (s *Store) ReposForClaimant(ctx context.Context, claimant ClaimIdentity, now time.Time) ([]string, error) {
	if !claimant.bound() {
		return nil, nil
	}
	rows, err := s.query(ctx, `
        SELECT DISTINCT runs.repo
          FROM runs
         WHERE EXISTS (SELECT 1 FROM nodes
                        WHERE nodes.run_id = runs.id
                          AND nodes.claim_principal = ? AND nodes.claim_token_prefix = ?
                          AND `+nodeClaimLiveSQL+`)
            OR EXISTS (SELECT 1 FROM triggers
                        WHERE triggers.id = runs.id
                          AND triggers.claim_principal = ? AND triggers.claim_token_prefix = ?
                          AND `+triggerClaimLiveSQL+`)`,
		claimant.Principal, claimant.TokenPrefix, now.UnixNano(),
		claimant.Principal, claimant.TokenPrefix, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, err
		}
		out = append(out, repo)
	}
	return out, rows.Err()
}
