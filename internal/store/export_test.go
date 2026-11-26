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

// ErrClaimCapacityUnavailableForTest distinguishes a full group from other claim refusals.
var ErrClaimCapacityUnavailableForTest = errClaimCapacityUnavailable

// SetTestClock overrides the store clock until the test ends.
func (s *Store) SetTestClock(t testing.TB, clock func() time.Time) {
	original, pinned := s.clock, s.pinnedClock
	t.Cleanup(func() { s.clock, s.pinnedClock = original, pinned })
	s.clock, s.pinnedClock = clock, s.now
}

// WithTestWallClock injects wall time without bypassing the production clock floor.
func WithTestWallClock(clock func() time.Time) Option {
	return Option{apply: func(s *Store) {
		s.clock, s.pinnedClock, s.wallClock = nil, nil, clock
	}}
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

const (
	schema43InvitationEmailLog     = "CREATE TABLE invitation_email_log (\n    address TEXT NOT NULL,\n    sent_at BIGINT NOT NULL,\n    inviter TEXT NOT NULL DEFAULT ''\n);\n\n"
	schema43InvitationEmailIndexes = "CREATE INDEX idx_invitation_email_log_address ON invitation_email_log(address, sent_at);\n\n" +
		"CREATE INDEX idx_invitation_email_log_inviter ON invitation_email_log(inviter, sent_at);\n\n"
	limitEventsTable = "CREATE TABLE limit_events (\n    kind TEXT NOT NULL CHECK (kind <> ''),\n    digest TEXT NOT NULL CHECK (length(digest) = 64),\n    at BIGINT NOT NULL\n);\n\n"
	limitEventsIndex = "CREATE INDEX idx_limit_events_key ON limit_events(kind, digest, at);\n\n"
)

// safety: the digest proves the fixture is the shipped schema 43 DDL, which keeps the invitation email log in place
// of limit_events.
func schema43NeutralForTest(t testing.TB) string {
	t.Helper()
	neutral := schemaNeutral
	for _, ddl := range []string{limitEventsTable, limitEventsIndex} {
		if !strings.Contains(neutral, ddl) {
			t.Fatalf("schema lacks %q", ddl)
		}
		neutral = strings.Replace(neutral, ddl, "", 1)
	}
	neutral = strings.Replace(neutral, "CREATE TABLE signup_gate (", schema43InvitationEmailLog+"CREATE TABLE signup_gate (", 1)
	neutral = strings.Replace(neutral, "CREATE INDEX idx_triggers_team_created", schema43InvitationEmailIndexes+"CREATE INDEX idx_triggers_team_created", 1)
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(neutral))); got != "7fe46a7554104b26ea25bac28ab7a719513991ad4f42af62a9cf7d9260bfc000" {
		t.Fatalf("schema 43 fixture digest %s", got)
	}
	return neutral
}

// safety: the digest proves the fixture is the shipped schema 42 DDL, whose balance index lacks the payment mode.
func schema42NeutralForTest(t testing.TB) string {
	t.Helper()
	neutral := strings.Replace(schema43NeutralForTest(t), "CREATE INDEX idx_credit_charges_team_mode_amount ON credit_charges(team, payment_mode, amount_micro);", "CREATE INDEX idx_credit_charges_team_amount ON credit_charges(team, amount_micro);", 1)
	neutral = strings.Replace(neutral, "CREATE INDEX idx_billing_awards_unsettled ON billing_awards(team) WHERE settled_at IS NULL;\n", "", 1)
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(neutral))); got != "0674849f24548cae23bd6c39f3634113014b66559f193af9522ed30a3dcb523d" {
		t.Fatalf("schema 42 fixture digest %s", got)
	}
	return neutral
}

// safety: the digest proves the fixture is the shipped schema 41 DDL, which lacks the invitation email inviter column.
func schema41NeutralForTest(t testing.TB) string {
	t.Helper()
	neutral := strings.Replace(schema42NeutralForTest(t), ",\n    inviter TEXT NOT NULL DEFAULT ''\n);", "\n);", 1)
	neutral = strings.Replace(neutral, "CREATE INDEX idx_invitation_email_log_inviter ON invitation_email_log(inviter, sent_at);\n\n", "", 1)
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(neutral))); got != "1bcc048252e13edb35021084dd24ad1d7f7adf9eb9cd9ee5fbc291a0ef7df189" {
		t.Fatalf("schema 41 fixture digest %s", got)
	}
	return neutral
}

// Schema38NeutralForTest returns the shipped schema 38 text, which lacks the run event tables.
func Schema38NeutralForTest(t testing.TB) string {
	t.Helper()
	schema41 := schema41NeutralForTest(t)
	i := strings.Index(schema41, "\n\n-- safety: a subscription secret is sealed")
	if i < 0 {
		t.Fatal("schema lacks the run event block")
	}
	neutral := schema41[:i] + "\n"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(neutral))); got != "d7d89dd6049c6139e01bae0a845983414393d5b7c622a2172c60fa1292e5d7da" {
		t.Fatalf("schema 38 fixture digest %s", got)
	}
	return neutral
}

// DropRunEventSchemaForTest returns a store to the schema 38 tables.
func DropRunEventSchemaForTest(t testing.TB, st *Store) {
	t.Helper()
	RewindToSchema41ForTest(t, st)
	for _, table := range []string{"audit_privacy_migrations", "run_event_deliveries", "run_event_subscriptions"} {
		if _, err := st.DB().ExecContext(t.Context(), `DROP TABLE `+table); err != nil {
			t.Fatal(err)
		}
	}
}

// SeedInvitationEmailsForTest records n invitation emails the account sent at at, each to its own address.
func SeedInvitationEmailsForTest(t testing.TB, st *Store, accountID string, n int, at time.Time) {
	t.Helper()
	for i := range n {
		address := fmt.Sprintf("seed-%s-%d@example.com", accountID, i)
		for kind, key := range map[string]string{invitationEmailsPerAccount.kind: accountID, invitationEmailsPerRecipient.kind: address} {
			if _, err := st.exec(t.Context(), `INSERT INTO limit_events (kind, digest, at) VALUES (?, ?, ?)`,
				kind, limitKeyDigest(key), at.UTC().UnixNano()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// RewindToSchema43ForTest returns a store to the schema 43 tables: the invitation email log in place of limit_events.
func RewindToSchema43ForTest(t testing.TB, st *Store) {
	t.Helper()
	statements := []string{`DROP TABLE limit_events`, strings.TrimSuffix(schema43InvitationEmailLog, ";\n\n")}
	statements = append(statements, strings.Split(strings.TrimSuffix(schema43InvitationEmailIndexes, ";\n\n"), ";\n\n")...)
	for _, statement := range statements {
		translated, err := translateSchema(statement, st.dialect)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().ExecContext(t.Context(), translated); err != nil {
			t.Fatal(err)
		}
	}
}

// RewindToSchema42ForTest returns a store to the schema 42 tables, whose credit and billing award indexes predate
// schema 43.
func RewindToSchema42ForTest(t testing.TB, st *Store) {
	t.Helper()
	RewindToSchema43ForTest(t, st)
	for _, statement := range []string{
		`DROP INDEX idx_billing_awards_unsettled`,
		`DROP INDEX idx_credit_charges_team_mode_amount`,
		`CREATE INDEX idx_credit_charges_team_amount ON credit_charges(team, amount_micro)`,
	} {
		if _, err := st.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
}

// RewindToSchema41ForTest returns a store to the schema 41 tables.
func RewindToSchema41ForTest(t testing.TB, st *Store) {
	t.Helper()
	RewindToSchema42ForTest(t, st)
	for _, statement := range []string{`DROP INDEX idx_invitation_email_log_inviter`, `ALTER TABLE invitation_email_log DROP COLUMN inviter`} {
		if _, err := st.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
}

// ClaimTeamInvitationForTest claims one of key's daily team invitations in its own transaction at the store's
// clock. A refusal reports the count that refused it.
func (s *Store) ClaimTeamInvitationForTest(ctx context.Context, key string) (used int, ok bool, err error) {
	return s.claimLimitForTest(ctx, limitClaim{teamInvitationsPerDay, key})
}

// ClaimUndeclaredLimitForTest claims an event under a limit missing from the declared list.
func (s *Store) ClaimUndeclaredLimitForTest(ctx context.Context, key string) (used int, ok bool, err error) {
	return s.claimLimitForTest(ctx, limitClaim{windowLimit{kind: "undeclared", limit: 1, window: time.Hour}, key})
}

func (s *Store) claimLimitForTest(ctx context.Context, claims ...limitClaim) (used int, ok bool, err error) {
	err = s.inTx(ctx, func(tx *storeTx) error {
		_, refused, err := claimWindowLimitsTx(ctx, tx, claims...)
		if refused != nil {
			used = refused.usage.count
		}
		ok = err == nil && refused == nil
		return err
	})
	return used, ok, err
}

// InvitationEmailAccountLockForTest names the advisory lock an email claim takes for accountID on PostgreSQL.
func InvitationEmailAccountLockForTest(accountID string) string {
	return "limit:" + invitationEmailsPerAccount.kind + ":" + limitKeyDigest(accountID)
}

// WindowLimitKindsForTest lists the kind of every declared window limit.
func WindowLimitKindsForTest() []string {
	kinds := make([]string, len(windowLimits))
	for i, l := range windowLimits {
		kinds[i] = l.kind
	}
	return kinds
}

// SeedLimitEventsForTest records n events for key under kind at at.
func SeedLimitEventsForTest(t testing.TB, st *Store, kind, key string, n int, at time.Time) {
	t.Helper()
	for range n {
		if _, err := st.exec(t.Context(), `INSERT INTO limit_events (kind, digest, at) VALUES (?, ?, ?)`,
			kind, limitKeyDigest(key), at.UTC().UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
}

// PrepareSchema36ForTest preserves configured rows in the shipped pre-migration schema.
func PrepareSchema36ForTest(t testing.TB, st *Store) {
	t.Helper()
	const trust = `CREATE TABLE team_build_trust (
    team TEXT NOT NULL REFERENCES teams(name) PRIMARY KEY,
    trust_local_builds BIGINT NOT NULL DEFAULT 0 CHECK(trust_local_builds IN (0,1))
);

`
	neutral := strings.Replace(strings.Replace(Schema38NeutralForTest(t), "    group_wait_started_at BIGINT CHECK(group_wait_started_at IS NULL OR group_wait_started_at>0),\n", "", 1), "CREATE TABLE audit_events (", trust+"CREATE TABLE audit_events (", 1)
	// safety: the digest proves the fixture is the shipped DDL, including both direct team foreign keys.
	neutral = strings.Replace(neutral, "-- safety: account audit events precede team creation, so this operator table permits no team.\n"+trust, trust+"-- safety: account audit events precede team creation, so this operator table permits no team.\n", 1)
	const digest = "afbe14b512e2e182e9f8322685477a29ce5fe337a01faf0003856ca317d23f24"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(neutral))); got != digest {
		t.Fatalf("schema 36 fixture digest %s, want %s", got, digest)
	}

	DropRunEventSchemaForTest(t, st)
	for _, statement := range []string{`ALTER TABLE nodes DROP COLUMN group_wait_started_at`, trust, `UPDATE sparkwing_schema_version SET version=36,baseline_digest='afbe14b512e2e182e9f8322685477a29ce5fe337a01faf0003856ca317d23f24'`} {
		if _, err := st.DB().ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
}

// ExerciseOIDCScrubGuardForTest checks the temporary guard inside a rolled-back migration transaction.
func ExerciseOIDCScrubGuardForTest(t testing.TB, s *Store, check func(func(string, ...any) error)) {
	t.Helper()
	tx, err := s.beginTx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackOrLog(tx)
	if _, err := tx.execContext(context.Background(), `UPDATE audit_privacy_migrations SET completed=0 WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	keyID, _, err := s.oidcAuditDigest("", "audience", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AsOperator().prepareOIDCScrub(context.Background(), tx, keyID); err != nil {
		t.Fatal(err)
	}
	if err := replaceAuditUpdateGuard(context.Background(), tx, oidcScrubAllowance(s.dialect)); err != nil {
		t.Fatal(err)
	}
	check(func(query string, args ...any) error {
		if _, err := tx.execContext(context.Background(), `SAVEPOINT probe`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.execContext(context.Background(), query, args...)
		if _, rollbackErr := tx.execContext(context.Background(), `ROLLBACK TO SAVEPOINT probe`); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if _, releaseErr := tx.execContext(context.Background(), `RELEASE SAVEPOINT probe`); releaseErr != nil {
			t.Fatal(releaseErr)
		}
		return err
	})
}
