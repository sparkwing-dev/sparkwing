package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Secret is one row in the secrets table. Masked controls log
// redaction; defaults to true. Pipeline is the owning pipeline name, or
// "" for an unscoped secret. Shared marks an unscoped secret a run may
// resolve; an unscoped row that is not shared answers admin only.
//
// The scope is the pipeline and not the repository, because a run's
// repository is a string its submitter typed and nothing proves the
// submitter owns it.
type Secret struct {
	Name      string
	Value     string
	Principal string
	Pipeline  string
	Masked    bool
	Shared    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateOrReplaceSecret upserts sec; created_at is preserved. Pipeline
// scopes the secret to one pipeline, and Shared opens an unscoped row to
// every run.
func (s *Store) CreateOrReplaceSecret(sec Secret, now time.Time) error {
	if sec.Name == "" {
		return errors.New("secrets: name required")
	}
	ts := now.UTC().Unix()
	_, err := s.execNoCtx(`
        INSERT INTO secrets (name, value, principal, masked, pipeline, shared, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(name, pipeline) DO UPDATE SET
            value = excluded.value,
            principal = excluded.principal,
            masked = excluded.masked,
            shared = excluded.shared,
            updated_at = excluded.updated_at
    `, sec.Name, sec.Value, sec.Principal, boolInt(sec.Masked), sec.Pipeline, boolInt(sec.Shared), ts, ts)
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
// pipeline, and ErrNotFound when there is none. Unlike
// GetSecretForPipeline it never falls back to the unscoped row.
func (s *Store) GetSecretRow(name, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	return s.readSecret(name, pipeline)
}

// GetSecretForPipeline returns the row named for pipeline, falling back
// to the unscoped row when the pipeline has none of its own. ErrNotFound
// when neither exists. This is the administrative read: it reaches an
// unscoped row whether or not it is shared.
func (s *Store) GetSecretForPipeline(name, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	if pipeline != "" {
		sec, err := s.readSecret(name, pipeline)
		if err == nil || !errors.Is(err, ErrNotFound) {
			return sec, err
		}
	}
	return s.readSecret(name, "")
}

// GetSecretForRun returns the row pipeline owns, falling back to an
// unscoped row only when that row is shared. ErrNotFound otherwise, so
// an unshared unscoped secret is indistinguishable from a missing one.
func (s *Store) GetSecretForRun(name, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	if pipeline != "" {
		sec, err := s.readSecret(name, pipeline)
		if err == nil || !errors.Is(err, ErrNotFound) {
			return sec, err
		}
	}
	sec, err := s.readSecret(name, "")
	if err != nil {
		return nil, err
	}
	if !sec.Shared {
		return nil, notFound("secret", name)
	}
	return sec, nil
}

func (s *Store) readSecret(name, pipeline string) (*Secret, error) {
	row := s.queryRowNoCtx(`
        SELECT name, value, principal, pipeline, masked, shared, created_at, updated_at
          FROM secrets
         WHERE name = ? AND pipeline = ?
    `, name, pipeline)
	var sec Secret
	var maskedInt, sharedInt int
	var created, updated int64
	err := row.Scan(&sec.Name, &sec.Value, &sec.Principal, &sec.Pipeline, &maskedInt, &sharedInt, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("secret", name)
		}
		return nil, err
	}
	sec.Masked = maskedInt != 0
	sec.Shared = sharedInt != 0
	sec.CreatedAt = time.Unix(created, 0).UTC()
	sec.UpdatedAt = time.Unix(updated, 0).UTC()
	return &sec, nil
}

// ListSecrets returns rows ordered by name then pipeline. HTTP handlers
// must blank Value before serializing.
func (s *Store) ListSecrets() ([]Secret, error) {
	rows, err := s.queryNoCtx(`
        SELECT name, value, principal, pipeline, masked, shared, created_at, updated_at
          FROM secrets
         ORDER BY name, pipeline
    `)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanSecretRows(rows)
}

// DeleteSecret removes the row owned by pipeline ("" for the unscoped
// row); ErrNotFound when missing.
func (s *Store) DeleteSecret(name, pipeline string) error {
	res, err := s.execNoCtx(`DELETE FROM secrets WHERE name = ? AND pipeline = ?`, name, pipeline)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return notFound("secret", name)
	}
	return nil
}

// PipelineForClaimedRun returns the pipeline of runID when claimant
// holds live work on it: an unexpired claim on one of its nodes, or the
// unexpired claim on the trigger that created it. ErrNotFound when the
// claimant holds neither, so a caller cannot name a run it is not
// executing.
func (s *Store) PipelineForClaimedRun(ctx context.Context, runID string, claimant ClaimIdentity, now time.Time) (string, error) {
	if !claimant.bound() || runID == "" {
		return "", ErrNotFound
	}
	var pipeline string
	err := s.queryRow(ctx, `
        SELECT runs.pipeline
          FROM runs
         WHERE runs.id = ?
           AND (EXISTS (SELECT 1 FROM nodes
                         WHERE nodes.run_id = runs.id
                           AND nodes.claim_principal = ? AND nodes.claim_token_prefix = ?
		                   AND `+nodeClaimLiveSQL("")+`)
             OR EXISTS (SELECT 1 FROM triggers
                         WHERE triggers.id = runs.id
                           AND triggers.claim_principal = ? AND triggers.claim_token_prefix = ?
		                   AND `+triggerClaimLiveSQL("")+`))`,
		runID,
		claimant.Principal, claimant.TokenPrefix, now.UnixNano(),
		claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&pipeline)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return pipeline, nil
}

// PipelinesForClaimant returns the distinct pipelines of the runs
// claimant currently holds work in, through node claims or trigger
// claims. Empty when it holds none.
func (s *Store) PipelinesForClaimant(ctx context.Context, claimant ClaimIdentity, now time.Time) ([]string, error) {
	if !claimant.bound() {
		return nil, nil
	}
	rows, err := s.query(ctx, `
        SELECT DISTINCT runs.pipeline
          FROM runs
         WHERE EXISTS (SELECT 1 FROM nodes
                        WHERE nodes.run_id = runs.id
                          AND nodes.claim_principal = ? AND nodes.claim_token_prefix = ?
		                  AND `+nodeClaimLiveSQL("")+`)
            OR EXISTS (SELECT 1 FROM triggers
                        WHERE triggers.id = runs.id
                          AND triggers.claim_principal = ? AND triggers.claim_token_prefix = ?
		                  AND `+triggerClaimLiveSQL("")+`)`,
		claimant.Principal, claimant.TokenPrefix, now.UnixNano(),
		claimant.Principal, claimant.TokenPrefix, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var pipeline string
		if err := rows.Scan(&pipeline); err != nil {
			return nil, err
		}
		out = append(out, pipeline)
	}
	return out, rows.Err()
}

// RotateSecretValues rewrites the stored value of every secret row with
// whatever reseal returns for it, in one transaction, so a key change
// either lands for the whole table or for none of it. Every other column
// keeps its value, including updated_at: the secret did not change, only
// the bytes it is stored as. Returns the number of rows rewritten.
//
// reseal sees the row as stored, which for an encrypted table means the
// envelope in Secret.Value; returning an error abandons the rotation.
func (s *Store) RotateSecretValues(ctx context.Context, reseal func(Secret) (string, error)) (rotated int, err error) {
	if reseal == nil {
		return 0, errors.New("secrets: reseal function required")
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackUnlessDone(tx, &err)

	// safety: SQLite serves one connection, so the whole table materializes before any write on this transaction.
	current, err := selectSecretsTx(ctx, tx)
	if err != nil {
		return 0, err
	}

	for _, sec := range current {
		value, rerr := reseal(sec)
		if rerr != nil {
			return 0, rerr
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE secrets SET value = ? WHERE name = ? AND pipeline = ?`,
			value, sec.Name, sec.Pipeline,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(current), nil
}

func selectSecretsTx(ctx context.Context, tx *storeTx) (secs []Secret, err error) {
	rows, err := tx.QueryContext(ctx, `
        SELECT name, value, principal, pipeline, masked, shared, created_at, updated_at
          FROM secrets
         ORDER BY name, pipeline`+tx.forUpdate())
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return scanSecretRows(rows)
}

func scanSecretRows(rows *sql.Rows) ([]Secret, error) {
	var out []Secret
	for rows.Next() {
		var sec Secret
		var maskedInt, sharedInt int
		var created, updated int64
		if err := rows.Scan(&sec.Name, &sec.Value, &sec.Principal, &sec.Pipeline,
			&maskedInt, &sharedInt, &created, &updated); err != nil {
			return nil, err
		}
		sec.Masked = maskedInt != 0
		sec.Shared = sharedInt != 0
		sec.CreatedAt = time.Unix(created, 0).UTC()
		sec.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, sec)
	}
	return out, rows.Err()
}
