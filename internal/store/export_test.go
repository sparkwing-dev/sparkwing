package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

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

// TestOnlySeedNode inserts a finished node with outcome into an existing run.
func (t *Tenant) TestOnlySeedNode(ctx context.Context, runID, nodeID, outcome string) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		_, err := tx.execContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json) VALUES (?,?,?,'done',?,1,?,?)`,
			t.team, runID, nodeID, outcome, []byte(`{"id":"`+nodeID+`"}`), []byte(`{}`))
		return err
	})
}
