package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

const teamInvitationsPerDay = 100

func fillTeamInvitations(t *testing.T, st *store.Store, key string, n int, at time.Time) {
	t.Helper()
	for i := range n {
		used, ok, err := st.ClaimTeamInvitationForTest(t.Context(), key, at)
		if err != nil || !ok || used != i {
			t.Fatalf("claim %d for %q = %d, %t, %v", i, key, used, ok, err)
		}
	}
}

func TestWindowLimitRefusesAtTheLimitAndRecordsNothing(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	fillTeamInvitations(t, st, "full", teamInvitationsPerDay, now)
	used, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "full", now)
	if err != nil || ok || used != teamInvitationsPerDay {
		t.Fatalf("claim past the limit = %d, %t, %v; want %d, false", used, ok, err, teamInvitationsPerDay)
	}
	if n := countWhere(t, st, "limit_events", "1=1"); n != teamInvitationsPerDay {
		t.Fatalf("log holds %d events after a refusal, want %d", n, teamInvitationsPerDay)
	}
}

func TestWindowLimitSlotReturnsWhenTheWindowRollsOver(t *testing.T) {
	st := storetest.Open(t)
	start := time.Unix(1_800_000_000, 0)
	fillTeamInvitations(t, st, "rolling", teamInvitationsPerDay, start)
	if used, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "rolling", start.Add(24*time.Hour-time.Nanosecond)); err != nil || ok || used != teamInvitationsPerDay {
		t.Fatalf("claim inside the window = %d, %t, %v; want refused", used, ok, err)
	}
	if used, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "rolling", start.Add(24*time.Hour)); err != nil || !ok || used != 0 {
		t.Fatalf("claim once the window passed = %d, %t, %v; want granted with 0 used", used, ok, err)
	}
}

func TestWindowLimitCountsEachKeySeparately(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	fillTeamInvitations(t, st, "busy", teamInvitationsPerDay, now)
	if used, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "quiet", now); err != nil || !ok || used != 0 {
		t.Fatalf("claim for another key = %d, %t, %v; want granted with 0 used", used, ok, err)
	}
}

func TestWindowLimitLogKeepsOnlyTheKeyDigest(t *testing.T) {
	st := storetest.Open(t)
	const key = "raw-team-name"
	if _, ok, err := st.ClaimTeamInvitationForTest(t.Context(), key, time.Now()); err != nil || !ok {
		t.Fatalf("claim = %t, %v", ok, err)
	}
	sum := sha256.Sum256([]byte(key))
	var kind, digest string
	if err := st.DB().QueryRowContext(t.Context(), `SELECT kind, digest FROM limit_events`).Scan(&kind, &digest); err != nil {
		t.Fatal(err)
	}
	if digest != hex.EncodeToString(sum[:]) || kind != "team_invitations" {
		t.Fatalf("logged kind %q and digest %q, want team_invitations and the SHA-256 of the key", kind, digest)
	}
}

// safety: gate must call start to release the racers, and return only once they may finish.
func raceForTheLastSlot(t *testing.T, st *store.Store, gate func(racers int, start func())) int {
	t.Helper()
	now := time.Now()
	fillTeamInvitations(t, st, "contended", teamInvitationsPerDay-1, now)
	const racers = 8
	start := make(chan struct{})
	type result struct {
		ok  bool
		err error
	}
	results := make(chan result, racers)
	for range racers {
		go func() {
			<-start
			_, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "contended", now)
			results <- result{ok, err}
		}()
	}
	gate(racers, func() { close(start) })
	wins := 0
	for range racers {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent claim: %v", r.err)
		}
		if r.ok {
			wins++
		}
	}
	if n := countWhere(t, st, "limit_events", "1=1"); n != teamInvitationsPerDay-1+wins {
		t.Fatalf("log holds %d events for %d grants", n, wins)
	}
	return wins
}

func TestWindowLimitGrantsTheLastSlotOnce(t *testing.T) {
	if wins := raceForTheLastSlot(t, storetest.OpenSQLite(t), func(_ int, start func()) { start() }); wins != 1 {
		t.Fatalf("%d racers took the last slot, want 1", wins)
	}
}

// The blocker lets every racer read the count before any can insert, the interleaving that would grant the slot twice.
func TestPostgresWindowLimitGrantsTheLastSlotToOneOfOverlappingClaims(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	wins := raceForTheLastSlot(t, st, func(racers int, start func()) {
		blocker, err := st.DB().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = blocker.Rollback() }()
		if _, err := blocker.ExecContext(ctx, `LOCK TABLE limit_events IN EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		start()
		for waiting := 0; waiting < racers; {
			if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
				WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			runtime.Gosched()
		}
	})
	if wins != 1 {
		t.Fatalf("%d racers took the last slot, want 1", wins)
	}
}

func TestRetentionDropsLimitEventsOnceTheirWindowPasses(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	fillTeamInvitations(t, st, "expired", 1, now.Add(-25*time.Hour))
	fillTeamInvitations(t, st, "live", 1, now.Add(-time.Hour))
	st.SetTestClock(t, func() time.Time { return now })
	if _, err := st.AsOperator().MaintainRetention(t.Context(), store.RetentionRequest{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if used, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "live", now); err != nil || !ok || used != 1 {
		t.Fatalf("live key after retention = %d, %t, %v; want its recent event kept", used, ok, err)
	}
	if n := countWhere(t, st, "limit_events", "1=1"); n != 2 {
		t.Fatalf("log holds %d events after retention, want the live key's 2", n)
	}
}

func TestRefusedInvitationLeavesTheDailySlot(t *testing.T) {
	st := storetest.Open(t)
	u := signIn(t, st, "s", "host@example.com")
	tn := tenant(t, st, u.PersonalTeam)
	if _, err := tn.InviteForTest(t, context.Background(), u.Account.ID, "host@example.com", store.RoleReader, time.Now()); !errors.Is(err, store.ErrAlreadyMember) {
		t.Fatalf("inviting a member = %v, want ErrAlreadyMember", err)
	}
	if n := countWhere(t, st, "limit_events", "1=1"); n != 0 {
		t.Fatalf("a refused invitation left %d events, want 0", n)
	}
}

func TestSchemaUpgradeMovesTheInvitationEmailLogIntoLimitEvents(t *testing.T) {
	digest := func(key string) string {
		sum := sha256.Sum256([]byte(key))
		return hex.EncodeToString(sum[:])
	}
	inbox, legacyInbox, account := digest("guest@example.com"), digest("legacy@example.com"), digest("inviting-account")
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			target := dialect.target(t)
			st := target.Open(t)
			store.RewindToSchema43ForTest(t, st)
			now := time.Now()
			for _, statement := range []struct {
				query string
				args  []any
			}{
				{`INSERT INTO invitation_email_log(address,inviter,sent_at) VALUES (?,?,?),(?,'',?)`, []any{inbox, account, now.Add(-time.Hour).UnixNano(), legacyInbox, now.Add(-2 * time.Hour).UnixNano()}},
				{`UPDATE sparkwing_schema_version SET version=43,baseline_digest='7fe46a7554104b26ea25bac28ab7a719513991ad4f42af62a9cf7d9260bfc000'`, nil},
			} {
				if _, err := st.DB().ExecContext(t.Context(), storetest.Rebind(st, statement.query), statement.args...); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded := target.Open(t)
			for where, want := range map[string]int{
				"kind = 'invitation_email_recipient' AND digest = '" + inbox + "'":       1,
				"kind = 'invitation_email_recipient' AND digest = '" + legacyInbox + "'": 1,
				"kind = 'invitation_email_account' AND digest = '" + account + "'":       1,
				"1 = 1": 3,
			} {
				if n := countWhere(t, upgraded, "limit_events", where); n != want {
					t.Errorf("upgraded log holds %d events where %s, want %d", n, where, want)
				}
			}
			if a, b := schemaShape(t, upgraded), schemaShape(t, dialect.target(t).Open(t)); a != b {
				t.Fatalf("schema upgrade differs: %s", shapeDiff(a, b))
			}
		})
	}
}
