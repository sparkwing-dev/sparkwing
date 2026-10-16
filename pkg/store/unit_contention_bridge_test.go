package store

import (
	"context"
	"testing"
	"time"
)

// TestOnlyClock sets the replica clock without changing database time.
func (s *Store) TestOnlyClock(now time.Time) { s.clock = func() time.Time { return now } }

// TestOnlyUnitDemand seeds a real claim fixture for either SQL dialect.
func (s *Store) TestOnlyUnitDemand(t *testing.T) UnitDemand {
	t.Helper()
	for _, team := range []string{"a", "b"} {
		if _, err := s.exec(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES (?,1,1) ON CONFLICT(name) DO NOTHING`, team); err != nil {
			t.Fatal(err)
		}
		if _, err := s.exec(t.Context(), `INSERT INTO team_counters(team) VALUES (?) ON CONFLICT(team) DO NOTHING`, team); err != nil {
			t.Fatal(err)
		}
	}
	_, demand := unitClaimFixtureStore(t, s)
	return demand
}

// TestOnlyDatabaseTime reads the authoritative clock used by leases.
func (s *Store) TestOnlyDatabaseTime(ctx context.Context) (time.Time, error) {
	return s.databaseNow(ctx)
}
