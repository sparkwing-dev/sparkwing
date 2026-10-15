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

// A signed-up team's secrets are rows the operator stores for free, so they
// are bounded in count and size; the operator's own team is not.
const (
	// MaxSecretsPerTeam bounds the secret rows one signed-up team holds.
	MaxSecretsPerTeam = 100
	// MaxSecretValueBytes bounds one stored secret value of a signed-up
	// team, as stored, sealed envelope included.
	MaxSecretValueBytes = 128 << 10
)

// ErrSecretLimit refuses a signed-up team's secret past [MaxSecretsPerTeam]
// or [MaxSecretValueBytes].
var ErrSecretLimit = errors.New("store: secret limit reached")

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetSecretRow returns t's row stored under exactly this name and
// pipeline, and ErrNotFound when there is none.
func (t *Tenant) GetSecretRow(name, pipeline string) (*Secret, error) {
	if name == "" {
		return nil, errors.New("secrets: name required")
	}
	return t.readSecret(name, pipeline)
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
	sec.CreatedAt = time.Unix(0, created).UTC()
	sec.UpdatedAt = time.Unix(0, updated).UTC()
	return &sec, nil
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

// ClaimedRun names the team and pipeline of a run a claimant holds live
// work in. A secret read for that claimant resolves in Team, never in the
// team of whoever asked, because the run's team is what owns its secrets.
type ClaimedRun struct {
	Team     Team
	Pipeline string
}

// ClaimedRunFor returns the team and pipeline of runID when claimant holds
// live work on it: an unexpired claim on one of its nodes, or the
// unexpired claim on the trigger that created it. A trigger created without
// its run, as a webhook's is, counts while its claimant is still fetching
// the source that creates the run; the trigger then names the team and
// pipeline. ErrNotFound when the claimant holds neither, so a caller cannot
// name a run it is not executing.
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
		// safety: only while no run row exists, so a run that does exist is
		// always judged, and its team read, by the query above.
		err = s.queryRow(ctx, `
        SELECT triggers.team, triggers.pipeline
          FROM triggers
         WHERE triggers.id = ?
           AND triggers.claim_principal = ? AND triggers.claim_token_prefix = ?
           AND `+triggerClaimLiveSQL("triggers.")+`
           AND NOT EXISTS (SELECT 1 FROM runs WHERE runs.id = triggers.id)`,
			runID, claimant.Principal, claimant.TokenPrefix, now.UnixNano()).Scan(&team, &run.Pipeline)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ClaimedRun{}, ErrNotFound
	}
	if err != nil {
		return ClaimedRun{}, err
	}
	run.Team = Team(team)
	return run, nil
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
	return scanTeamSecretRows(rows)
}

func scanTeamSecretRows(rows *sql.Rows) ([]TeamSecret, error) {
	var out []TeamSecret
	for rows.Next() {
		var row TeamSecret
		var maskedInt, sharedInt int
		var created, updated int64
		if err := rows.Scan(&row.Team, &row.Secret.Name, &row.Secret.Value, &row.Secret.Principal,
			&row.Secret.Pipeline, &maskedInt, &sharedInt, &created, &updated); err != nil {
			return nil, err
		}
		row.Secret.Masked = maskedInt != 0
		row.Secret.Shared = sharedInt != 0
		row.Secret.CreatedAt = time.Unix(0, created).UTC()
		row.Secret.UpdatedAt = time.Unix(0, updated).UTC()
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
		sec.CreatedAt = time.Unix(0, created).UTC()
		sec.UpdatedAt = time.Unix(0, updated).UTC()
		out = append(out, sec)
	}
	return out, rows.Err()
}
