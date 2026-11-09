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
	request := runspec.Request{Pipeline: pipeline, Git: runspec.Git{SHA: sha, Repository: runspec.Repository{Key: repo}}, Options: options, Env: runspec.EnvSnapshot{Names: env}}
	body, err := json.Marshal(runspec.SealedRequest{Version: 1, Request: request, Provenance: provenance, ProofEligible: request.ProofEligible()})
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

func InstallSchema35ActionsForTest(t testing.TB, s *Store) {
	t.Helper()
	for _, ddl := range schema35ActionsStatements {
		translated, err := translateSchema(ddl, s.dialect)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(translated); err != nil {
			t.Fatal(err)
		}
	}
}

// OpenSchema36ForTest creates the shipped schema before any current migration runs.
func OpenSchema36ForTest(t testing.TB, dialect Dialect, target string, opts ...Option) *Store {
	t.Helper()
	const trust = `CREATE TABLE team_build_trust (
    team TEXT NOT NULL REFERENCES teams(name) PRIMARY KEY,
    trust_local_builds BIGINT NOT NULL DEFAULT 0 CHECK(trust_local_builds IN (0,1))
);

`
	neutral := strings.Replace(schemaNeutral, "CREATE TABLE audit_events (", trust+"CREATE TABLE audit_events (", 1)
	// safety: the digest proves the fixture is the shipped DDL, including both direct team foreign keys.
	neutral = strings.Replace(neutral, "-- safety: account audit events precede team creation, so this operator table permits no team.\n"+trust, trust+"-- safety: account audit events precede team creation, so this operator table permits no team.\n", 1)
	const digest = "afbe14b512e2e182e9f8322685477a29ce5fe337a01faf0003856ca317d23f24"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(neutral))); got != digest {
		t.Fatalf("schema 36 fixture digest %s, want %s", got, digest)
	}
	db, err := rawDBForTest(dialect, target)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	st := &Store{db: db, dialect: dialect}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	tx, err := st.beginTx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	translated, err := translateSchema(neutral, dialect)
	if err == nil {
		err = execStatements(t.Context(), tx, translated)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.operator.installAuditProtection(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.execContext(t.Context(), `INSERT INTO sparkwing_schema_version(version,applied_at,baseline_digest) VALUES(36,1,?)`, digest); err != nil {
		t.Fatal(err)
	}
	if err := recordDefaultTeamTx(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := seedSignUpGateTx(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := st.configurePolicyOptions(opts); err != nil {
		t.Fatal(err)
	}
	return st
}
