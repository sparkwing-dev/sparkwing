package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
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
