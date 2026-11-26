package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

const teamInvitationsPerDay = 100

func claimTeamInvitationAt(t *testing.T, st *store.Store, key string, at time.Time) (int, bool, error) {
	t.Helper()
	st.SetTestClock(t, func() time.Time { return at })
	return st.ClaimTeamInvitationForTest(t.Context(), key)
}

func fillTeamInvitations(t *testing.T, st *store.Store, key string, n int, at time.Time) {
	t.Helper()
	st.SetTestClock(t, func() time.Time { return at })
	for i := range n {
		if _, ok, err := st.ClaimTeamInvitationForTest(t.Context(), key); err != nil || !ok {
			t.Fatalf("claim %d for %q = %t, %v", i, key, ok, err)
		}
	}
}

func TestWindowLimitRefusesAtTheLimitAndRecordsNothing(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	fillTeamInvitations(t, st, "full", teamInvitationsPerDay, now)
	used, ok, err := claimTeamInvitationAt(t, st, "full", now)
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
	if used, ok, err := claimTeamInvitationAt(t, st, "rolling", start.Add(24*time.Hour-time.Nanosecond)); err != nil || ok || used != teamInvitationsPerDay {
		t.Fatalf("claim inside the window = %d, %t, %v; want refused", used, ok, err)
	}
	if _, ok, err := claimTeamInvitationAt(t, st, "rolling", start.Add(24*time.Hour)); err != nil || !ok {
		t.Fatalf("claim once the window passed = %t, %v; want granted", ok, err)
	}
}

func TestWindowLimitCountsEachKeySeparately(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	fillTeamInvitations(t, st, "busy", teamInvitationsPerDay, now)
	if _, ok, err := claimTeamInvitationAt(t, st, "quiet", now); err != nil || !ok {
		t.Fatalf("claim for another key = %t, %v; want granted", ok, err)
	}
}

func TestWindowLimitLogKeepsOnlyTheKeyDigest(t *testing.T) {
	st := storetest.Open(t)
	const key = "raw-team-name"
	if _, ok, err := st.ClaimTeamInvitationForTest(t.Context(), key); err != nil || !ok {
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
			_, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "contended")
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
	if _, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "live"); err != nil || !ok {
		t.Fatalf("live key after retention = %t, %v; want granted", ok, err)
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

func TestWindowLimitRefusesAnUndeclaredLimit(t *testing.T) {
	st := storetest.Open(t)
	if _, ok, err := st.ClaimUndeclaredLimitForTest(t.Context(), "key"); err == nil || ok {
		t.Fatalf("claim under an undeclared limit = %t, %v; want an error", ok, err)
	}
	if n := countWhere(t, st, "limit_events", "1 = 1"); n != 0 {
		t.Fatalf("a refused undeclared limit left %d events, want 0", n)
	}
}

func TestWindowLimitKindsAreUnique(t *testing.T) {
	kinds := store.WindowLimitKindsForTest()
	for i, kind := range kinds {
		if kind == "" || slices.Contains(kinds[i+1:], kind) {
			t.Fatalf("declared limit kinds %q repeat or leave one empty", kinds)
		}
	}
}

func TestSchemaUpgradeKeepsEachTeamsDailyInvitationCount(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			target := dialect.target(t)
			st := target.Open(t)
			now := time.Now()
			if _, err := st.DB().ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('kept',1,1)`); err != nil {
				t.Fatal(err)
			}
			insert := storetest.Rebind(st, `INSERT INTO invitations(id,team,email,role,invited_by,created_at,expires_at,withdrawn_at) VALUES (?,'kept',?,'reader','owner',?,?,?)`)
			for i := range teamInvitationsPerDay + 1 {
				at := now.Add(-time.Hour)
				if i == teamInvitationsPerDay {
					at = now.Add(-25 * time.Hour)
				}
				if _, err := st.DB().ExecContext(t.Context(), insert, fmt.Sprintf("inv-%d", i), fmt.Sprintf("guest-%d@example.com", i),
					at.UnixNano(), at.Add(7*24*time.Hour).UnixNano(), at.UnixNano()); err != nil {
					t.Fatal(err)
				}
			}
			store.RewindToSchema43ForTest(t, st)
			if _, err := st.DB().ExecContext(t.Context(), `UPDATE sparkwing_schema_version SET version=43,baseline_digest='7fe46a7554104b26ea25bac28ab7a719513991ad4f42af62a9cf7d9260bfc000'`); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded := target.Open(t)
			if used, ok, err := claimTeamInvitationAt(t, upgraded, "kept", now); err != nil || ok || used != teamInvitationsPerDay {
				t.Fatalf("invitation after the upgrade = %d, %t, %v; want refused at %d", used, ok, err, teamInvitationsPerDay)
			}
		})
	}
}

func TestRecipientRefusalLeavesTheInvitingAccountsSlot(t *testing.T) {
	st := storetest.Open(t)
	now := time.Now()
	owner := signIn(t, st, "o", "owner@example.com")
	store.SeedInvitationEmailsForTest(t, st, owner.Account.ID, store.MaxInvitationEmailsPerAccountPerDay-1, now)
	store.SeedLimitEventsForTest(t, st, "invitation_email_recipient", "full@example.com", store.MaxInvitationEmailsPerDay, now)
	full := inviteAt(t, st, owner.Account.ID, owner.PersonalTeam, "full@example.com", now)
	if withheld, err := claimReasonAt(t, st, full, now); err != nil || withheld != store.InvitationEmailRecipientLimit {
		t.Fatalf("email to a full inbox = %q, %v; want withheld for the recipient", withheld, err)
	}
	last := inviteAt(t, st, owner.Account.ID, owner.PersonalTeam, "last@example.com", now)
	if ok, err := claimAt(t, st, last, now); err != nil || !ok {
		t.Fatalf("the account's last email after a recipient refusal = %t, %v; want sent", ok, err)
	}
}

// The account lock is held while the oldest send leaves the window, so a claim that read the clock before waiting
// would refuse with a retry time already past.
func TestPostgresInvitationEmailClaimReadsTheClockAfterItsLocks(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	owner := signIn(t, st, "o", "owner@example.com")
	start := time.Now()
	var clock atomic.Int64
	clock.Store(start.Add(24*time.Hour - time.Second).UnixNano())
	st.SetTestClock(t, func() time.Time { return time.Unix(0, clock.Load()) })
	store.SeedInvitationEmailsForTest(t, st, owner.Account.ID, store.MaxInvitationEmailsPerAccountPerDay, start)
	inv, err := tenant(t, st, owner.PersonalTeam).InviteForTest(t, ctx, owner.Account.ID, "waiting@example.com", store.RoleReader, time.Unix(0, clock.Load()))
	if err != nil {
		t.Fatal(err)
	}
	st.SetTestClock(t, func() time.Time { return time.Unix(0, clock.Load()) })
	blocker, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, store.InvitationEmailAccountLockForTest(owner.Account.ID)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		withheld store.InvitationEmailWithheld
		err      error
	}
	done := make(chan result, 1)
	go func() {
		withheld, err := st.AsOperator().ClaimInvitationEmail(ctx, inv.ID)
		done <- result{withheld, err}
	}()
	for waiting := 0; waiting == 0; {
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	clock.Store(start.Add(24*time.Hour + time.Second).UnixNano())
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.err != nil || r.withheld != "" {
		t.Fatalf("claim after the oldest send left the window = %q, %v; want sent", r.withheld, r.err)
	}
}

func TestWindowLimitRefusesAKeyNamedTwice(t *testing.T) {
	st := storetest.Open(t)
	if _, ok, err := st.ClaimTeamInvitationForTest(t.Context(), "twice", "twice"); err == nil || ok {
		t.Fatalf("claim naming one key twice = %t, %v; want an error", ok, err)
	}
	if n := countWhere(t, st, "limit_events", "1 = 1"); n != 0 {
		t.Fatalf("a claim naming one key twice left %d events, want 0", n)
	}
}

// The first lock in sorted order is held, and the request names the other key first; a claim that locked in the
// order given would hold the other key while it waits.
func TestPostgresWindowLimitLocksKeysInSortedOrder(t *testing.T) {
	st := storetest.OpenPostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	first, second := "alpha", "beta"
	if store.TeamInvitationLockForTest(second) < store.TeamInvitationLockForTest(first) {
		first, second = second, first
	}
	blocker, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, store.TeamInvitationLockForTest(first)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, ok, err := st.ClaimTeamInvitationForTest(ctx, second, first)
		if err == nil && !ok {
			err = errors.New("claim refused")
		}
		done <- err
	}()
	for waiting := 0; waiting == 0; {
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	probe, err := st.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	var free bool
	if err := probe.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, store.TeamInvitationLockForTest(second)).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if free {
		if _, err := probe.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, store.TeamInvitationLockForTest(second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("claim after the blocker released = %v", err)
	}
	if !free {
		t.Fatal("the claim held the later key while it waited for the earlier one")
	}
}
