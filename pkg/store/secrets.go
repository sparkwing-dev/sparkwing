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

// CreateOrReplaceSecret upserts sec into the default team.
func (s *Store) CreateOrReplaceSecret(sec Secret, now time.Time) error {
	return s.defaultTenant().CreateOrReplaceSecret(sec, now)
}

// CreateOrReplaceSecret upserts sec into t's team; created_at is
// preserved. Pipeline scopes the secret to one pipeline, and Shared
// opens an unscoped row to every run.
//
// The team is in the conflict target because it leads the primary key,
// so a name another team already used names a different row rather than
// that team's.
func (t *Tenant) CreateOrReplaceSecret(sec Secret, now time.Time) error {
	if sec.Name == "" {
		return errors.New("secrets: name required")
	}
	ts := now.UTC().Unix()
	_, err := t.s.execNoCtx(`
        INSERT INTO secrets (team, name, value, principal, masked, pipeline, shared, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(team, name, pipeline) DO UPDATE SET
            value = excluded.value,
            principal = excluded.principal,
            masked = excluded.masked,
            shared = excluded.shared,
            updated_at = excluded.updated_at
    `, string(t.team), sec.Name, sec.Value, sec.Principal, boolInt(sec.Masked), sec.Pipeline, boolInt(sec.Shared), ts, ts)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetSecret returns the default team's unscoped row including Value.
func (s *Store) GetSecret(name string) (*Secret, error) {
	return s.defaultTenant().GetSecret(name)
}

// GetSecret returns t's unscoped row including Value.
func (t *Tenant) GetSecret(name string) (*Secret, error) {
	return t.readSecret(name, "")
}

// GetSecretRow returns the row stored under exactly this name and
// pipeline, and ErrNotFound when there is none. Unlike
// GetSecretForPipeline it never falls back to the unscoped row.
func (s *Store) GetSecretRow(name, pipeline string) (*Secret, error) {
	return s.defaultTenant().GetSecretRow(name, pipeline)
}

// GetSecretRow returns t's row stored under exactly this name and
// pipeline, and ErrNotFound when there is none.
func (t *Tenant) GetSecretRow(name, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	return t.readSecret(name, pipeline)
}

// GetSecretForPipeline returns the row named for pipeline, falling back
// to the unscoped row when the pipeline has none of its own. ErrNotFound
// when neither exists. This is the administrative read: it reaches an
// unscoped row whether or not it is shared.
func (s *Store) GetSecretForPipeline(name, pipeline string) (*Secret, error) {
	return s.defaultTenant().GetSecretForPipeline(name, pipeline)
}

// GetSecretForPipeline returns t's row named for pipeline, falling back
// to t's unscoped row when the pipeline has none of its own.
func (t *Tenant) GetSecretForPipeline(name, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	if pipeline != "" {
		sec, err := t.readSecret(name, pipeline)
		if err == nil || !errors.Is(err, ErrNotFound) {
			return sec, err
		}
	}
	return t.readSecret(name, "")
}

// GetSecretForRun returns the row pipeline owns, falling back to an
// unscoped row only when that row is shared. ErrNotFound otherwise, so
// an unshared unscoped secret is indistinguishable from a missing one.
func (s *Store) GetSecretForRun(name, pipeline string) (*Secret, error) {
	return s.defaultTenant().GetSecretForRun(name, pipeline)
}

// GetSecretForRun returns t's row that pipeline owns, falling back to
// t's unscoped row only when that row is shared.
func (t *Tenant) GetSecretForRun(name, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	if pipeline != "" {
		sec, err := t.readSecret(name, pipeline)
		if err == nil || !errors.Is(err, ErrNotFound) {
			return sec, err
		}
	}
	sec, err := t.readSecret(name, "")
	if err != nil {
		return nil, err
	}
	if !sec.Shared {
		return nil, notFound("secret", name)
	}
	return sec, nil
}

func (t *Tenant) readSecret(name, pipeline string) (*Secret, error) {
	row := t.s.queryRowNoCtx(`
        SELECT name, value, principal, pipeline, masked, shared, created_at, updated_at
          FROM secrets
         WHERE team = ? AND name = ? AND pipeline = ?
    `, string(t.team), name, pipeline)
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

// ListSecrets returns the default team's rows.
func (s *Store) ListSecrets() ([]Secret, error) {
	return s.defaultTenant().ListSecrets()
}

// ListSecrets returns t's rows ordered by name then pipeline. HTTP
// handlers must blank Value before serializing.
func (t *Tenant) ListSecrets() ([]Secret, error) {
	rows, err := t.s.queryNoCtx(`
        SELECT name, value, principal, pipeline, masked, shared, created_at, updated_at
          FROM secrets
         WHERE team = ?
         ORDER BY name, pipeline
    `, string(t.team))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanSecretRows(rows)
}

// DeleteSecret removes the default team's row.
func (s *Store) DeleteSecret(name, pipeline string) error {
	return s.defaultTenant().DeleteSecret(name, pipeline)
}

// DeleteSecret removes t's row owned by pipeline ("" for the unscoped
// row); ErrNotFound when missing.
func (t *Tenant) DeleteSecret(name, pipeline string) error {
	res, err := t.s.execNoCtx(
		`DELETE FROM secrets WHERE team = ? AND name = ? AND pipeline = ?`,
		string(t.team), name, pipeline)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return notFound("secret", name)
	}
	return nil
}

// ClaimedRun names the team and pipeline of a run a claimant holds live
// work in. A secret read for that claimant resolves in Team, never in the
// team of whoever asked, because the run's team is what owns its secrets.
type ClaimedRun struct {
	Team     Team
	Pipeline string
}

// ClaimedRunFor returns the team and pipeline of runID when claimant holds
// live work on it: an unexpired claim on one of its nodes, or the
// unexpired claim on the trigger that created it. ErrNotFound when the
// claimant holds neither, so a caller cannot name a run it is not
// executing.
func (s *Store) ClaimedRunFor(ctx context.Context, runID string, claimant ClaimIdentity, now time.Time) (ClaimedRun, error) {
	if !claimant.bound() || runID == "" {
		return ClaimedRun{}, ErrNotFound
	}
	var run ClaimedRun
	var team string
	err := s.queryRow(ctx, `
        SELECT runs.team, runs.pipeline
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
		claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&team, &run.Pipeline)
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimedRun{}, ErrNotFound
	}
	if err != nil {
		return ClaimedRun{}, err
	}
	run.Team = Team(team)
	return run, nil
}

// ClaimedRunsFor returns the distinct team and pipeline pairs of the runs
// claimant currently holds work in, through node claims or trigger
// claims. Empty when it holds none.
func (s *Store) ClaimedRunsFor(ctx context.Context, claimant ClaimIdentity, now time.Time) (_ []ClaimedRun, err error) {
	if !claimant.bound() {
		return nil, nil
	}
	rows, err := s.query(ctx, `
        SELECT DISTINCT runs.team, runs.pipeline
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
	defer closeRowsInto(rows, &err)
	var out []ClaimedRun
	for rows.Next() {
		var run ClaimedRun
		var team string
		if err := rows.Scan(&team, &run.Pipeline); err != nil {
			return nil, err
		}
		run.Team = Team(team)
		out = append(out, run)
	}
	return out, rows.Err()
}

// RotateSecretValues rewrites the stored value of every secret row with
// whatever reseal returns for it, in one transaction, so a key change
// either lands for the whole table or for none of it. Every other column
// keeps its value, including updated_at: the secret did not change, only
// the bytes it is stored as. Returns the number of rows rewritten.
//
// reseal sees the owning team and the row as stored, which for an
// encrypted table means the envelope in Secret.Value; returning an error
// abandons the rotation.
func (s *Store) RotateSecretValues(ctx context.Context, reseal func(Team, Secret) (string, error)) (rotated int, err error) {
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

	for _, row := range current {
		value, rerr := reseal(Team(row.team), row.Secret)
		if rerr != nil {
			return 0, rerr
		}
		// safety: the team is in the predicate because the name and the
		// pipeline no longer identify one row, and without it a rotation
		// would reseal every team's secret of that name with one team's key.
		if _, err := tx.ExecContext(ctx,
			`UPDATE secrets SET value = ? WHERE team = ? AND name = ? AND pipeline = ?`,
			value, row.team, row.Secret.Name, row.Secret.Pipeline,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(current), nil
}

// SecretResealCounts is what one [Store.ResealSecrets] pass did.
type SecretResealCounts struct {
	// Resealed rows now hold what reseal returned for them.
	Resealed int
	// Skipped rows are ones reseal refused; they keep their stored value.
	Skipped int
	// Raced rows changed between the read and the write, so the pass left
	// them to whoever changed them.
	Raced int
}

// ResealSecrets walks every team's secret rows whose stored value does not
// begin with sealedPrefix, batchSize rows at a time in (team, name,
// pipeline) order, and stores what reseal returns for each. A row reseal
// errors on keeps its value and is counted skipped, so one unreadable row
// does not hold back the rest. Each write names the value it replaces, so a
// row rewritten meanwhile by a request or another controller keeps that
// newer value. Running it again finds only the rows an earlier pass
// skipped, which makes it safe to run on every start.
//
// Unlike [Store.RotateSecretValues] it holds no transaction across the
// table, because each row it touches moves from a state every reader
// refuses to one every reader accepts, and a large table would otherwise
// be locked for the whole pass.
func (s *Store) ResealSecrets(ctx context.Context, sealedPrefix string, batchSize int, reseal func(Team, Secret) (string, error)) (SecretResealCounts, error) {
	var counts SecretResealCounts
	if reseal == nil {
		return counts, errors.New("secrets: reseal function required")
	}
	if sealedPrefix == "" {
		return counts, errors.New("secrets: sealed prefix required")
	}
	if batchSize < 1 {
		return counts, errors.New("secrets: batch size must be at least 1")
	}
	var after *teamSecret
	for {
		batch, err := s.secretsNotSealed(ctx, sealedPrefix, after, batchSize)
		if err != nil {
			return counts, err
		}
		for i := range batch {
			row := &batch[i]
			value, rerr := reseal(Team(row.team), row.Secret)
			if rerr != nil {
				counts.Skipped++
				continue
			}
			res, err := s.exec(ctx,
				`UPDATE secrets SET value = ? WHERE team = ? AND name = ? AND pipeline = ? AND value = ?`,
				value, row.team, row.Secret.Name, row.Secret.Pipeline, row.Secret.Value)
			if err != nil {
				return counts, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return counts, err
			}
			if n == 0 {
				counts.Raced++
				continue
			}
			counts.Resealed++
		}
		if len(batch) < batchSize {
			return counts, nil
		}
		after = &batch[len(batch)-1]
	}
}

func (s *Store) secretsNotSealed(ctx context.Context, sealedPrefix string, after *teamSecret, limit int) (_ []teamSecret, err error) {
	q := `
        SELECT team, name, value, principal, pipeline, masked, shared, created_at, updated_at
          FROM secrets
         WHERE substr(value, 1, ?) <> ?`
	args := []any{len(sealedPrefix), sealedPrefix}
	if after != nil {
		q += `
           AND (team > ? OR (team = ? AND (name > ? OR (name = ? AND pipeline > ?))))`
		args = append(args, after.team, after.team, after.Secret.Name, after.Secret.Name, after.Secret.Pipeline)
	}
	q += `
         ORDER BY team, name, pipeline
         LIMIT ?`
	args = append(args, limit)
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return scanTeamSecretRows(rows)
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
func (s *Store) SampleSealedSecrets(ctx context.Context, sealedPrefix string, limit int) (_ []TeamSecret, err error) {
	if sealedPrefix == "" || limit < 1 {
		return nil, errors.New("secrets: sample needs a prefix and a positive limit")
	}
	rows, err := s.query(ctx, `
        SELECT team, name, value, principal, pipeline, masked, shared, created_at, updated_at
          FROM secrets
         WHERE substr(value, 1, ?) = ?
         ORDER BY updated_at DESC, team, name, pipeline
         LIMIT ?`, len(sealedPrefix), sealedPrefix, limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	scanned, err := scanTeamSecretRows(rows)
	if err != nil {
		return nil, err
	}
	out := make([]TeamSecret, 0, len(scanned))
	for _, row := range scanned {
		out = append(out, TeamSecret{Team: Team(row.team), Secret: row.Secret})
	}
	return out, nil
}

// safety: the team is not a field of [Secret], because that is the shape
// a tenant reads and writes and a tenant never names its own team; a
// rotation across every team still has to write each row back under the
// team that owns it.
type teamSecret struct {
	Secret
	team string
}

func selectSecretsTx(ctx context.Context, tx *storeTx) (secs []teamSecret, err error) {
	rows, err := tx.QueryContext(ctx, `
        SELECT team, name, value, principal, pipeline, masked, shared, created_at, updated_at
          FROM secrets
         ORDER BY team, name, pipeline`+tx.forUpdate())
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return scanTeamSecretRows(rows)
}

func scanTeamSecretRows(rows *sql.Rows) ([]teamSecret, error) {
	var out []teamSecret
	for rows.Next() {
		var row teamSecret
		var maskedInt, sharedInt int
		var created, updated int64
		if err := rows.Scan(&row.team, &row.Secret.Name, &row.Secret.Value, &row.Secret.Principal,
			&row.Secret.Pipeline, &maskedInt, &sharedInt, &created, &updated); err != nil {
			return nil, err
		}
		row.Secret.Masked = maskedInt != 0
		row.Secret.Shared = sharedInt != 0
		row.Secret.CreatedAt = time.Unix(created, 0).UTC()
		row.Secret.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, row)
	}
	return out, rows.Err()
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
