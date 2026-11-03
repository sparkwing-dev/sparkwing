package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// DB exposes the store's own pool to this package's tests.
func (s *Store) DB() *sql.DB { return s.db }

// SetTestClock overrides the store clock until the test ends.
func (s *Store) SetTestClock(t testing.TB, clock func() time.Time) {
	original, pinned := s.clock, s.pinnedClock
	t.Cleanup(func() { s.clock, s.pinnedClock = original, pinned })
	s.clock, s.pinnedClock = clock, s.now
}

// SetTestBusyTimeout makes every SQLite store this process opens use ms as
// its busy_timeout until the test ends.
func SetTestBusyTimeout(t testing.TB, ms int) {
	original := busyTimeoutMS
	t.Cleanup(func() { busyTimeoutMS = original })
	busyTimeoutMS = ms
}

// TestOnlySeedGitHubInstallation binds an installation with no webhook or connect authority.
func (t *Tenant) TestOnlySeedGitHubInstallation(ctx context.Context, in GitHubAppInstallation, now time.Time) (GitHubAppInstallation, error) {
	var out GitHubAppInstallation
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		out, err = t.bindGitHubInstallationTx(ctx, tx, in, now)
		return err
	})
	return out, err
}

// TestOnlyCreateTeam registers a team with no account and no tombstone check.
func (o *Operator) TestOnlyCreateTeam(ctx context.Context, team Team) error {
	team = NormalizeTeam(team)
	if team == "" {
		return ErrNoTeam
	}
	return o.s.inTx(ctx, func(tx *storeTx) error { return registerTeamTx(ctx, tx, team) })
}

// CloudSizeLabelSQL exposes the claim query's size-label rule to the dialect parity test.
var CloudSizeLabelSQL = cloudSizeLabelSQL

// TestOnlySeedSealedRun inserts a successful executed run with a sealed
// provenance, options and submitted environment names, bypassing intake.
func (t *Tenant) TestOnlySeedSealedRun(ctx context.Context, id, pipeline string, repo runspec.RepoKey, sha string, provenance runspec.Provenance, options runspec.Options, finished time.Time, env ...string) error {
	body, err := json.Marshal(runspec.SealedRequest{Version: 1, Request: runspec.Request{Pipeline: pipeline, Git: runspec.Git{SHA: sha, Repository: runspec.Repository{Key: repo}}, Options: options, Env: runspec.EnvSnapshot{Names: env}}, Provenance: provenance})
	if err != nil {
		return err
	}
	return t.s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.execContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,git_sha,provenance_sha,repo_key,dispatch,intake_mode,created_subject_kind,created_principal,created_at,started_at,finished_at,log_retention_class,log_retention_days)
			SELECT ?,?,COALESCE(MAX(number),0)+1,?,'success',?,?,1,?,?,?,'host','execute','account','fixture',?,?,?,'r30',30 FROM runs WHERE team=?`,
			t.team, id, pipeline, body, id, sha, provenance.SHA(), string(repo), finished.UnixNano(), finished.UnixNano(), finished.UnixNano(), t.team)
		return err
	})
}

// TestOnlySeedNode inserts a finished node with outcome into an existing run,
// recording whether it ran on a trusted host.
func (t *Tenant) TestOnlySeedNode(ctx context.Context, runID, nodeID, outcome string, trustedHost bool) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.execContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json,trusted_host) VALUES (?,?,?,'done',?,1,?,?,?)`,
			t.team, runID, nodeID, outcome, []byte(`{"id":"`+nodeID+`"}`), []byte(`{}`), boolInt(trustedHost))
		return err
	})
}

// CreateReleasedStore creates a store from a release's neutral schema at
// version in an empty database, as that release's first open did, then runs seed.
func CreateReleasedStore(ctx context.Context, dialect Dialect, target, neutral string, version int, seed ...string) (err error) {
	db, err := rawDBForTest(dialect, target)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	s := &Store{db: db, dialect: dialect}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	translated, err := translateSchema(neutral, dialect)
	if err != nil {
		return err
	}
	if err := execStatements(ctx, tx, translated); err != nil {
		return err
	}
	if err := tx.operator.installAuditProtection(ctx, tx); err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ReplaceAll(neutral, "\r\n", "\n"))))
	if _, err := tx.execContext(ctx, `INSERT INTO sparkwing_schema_version (version, applied_at, baseline_digest) VALUES (?, ?, ?)`, version, time.Now().UnixNano(), digest); err != nil {
		return err
	}
	if err := recordDefaultTeamTx(ctx, tx); err != nil {
		return err
	}
	if err := seedSignUpGateTx(ctx, tx); err != nil {
		return err
	}
	for _, statement := range seed {
		if _, err := tx.execContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ExecRawForTest runs statement against the database without opening it as a
// store, so the store's own open cannot upgrade or refuse it first.
func ExecRawForTest(ctx context.Context, dialect Dialect, target, statement string) (err error) {
	db, err := rawDBForTest(dialect, target)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	_, err = db.ExecContext(ctx, statement)
	return err
}

func rawDBForTest(dialect Dialect, target string) (*sql.DB, error) {
	if dialect == DialectPostgres {
		cfg, err := postgresConfig(target)
		if err != nil {
			return nil, err
		}
		return stdlib.OpenDB(*cfg), nil
	}
	return sql.Open("sqlite", sqliteDSN(target))
}
