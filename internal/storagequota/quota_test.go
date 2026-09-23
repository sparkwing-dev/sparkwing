package storagequota_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

type counts struct {
	mu   sync.Mutex
	used map[string]int64
}

func (c *counts) get(team string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used[team]
}

func (c *counts) add(team string, n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.used[team] += n
}

type lookups struct {
	mu      sync.Mutex
	answers map[string]storagequota.Standing
	fail    bool
	calls   int
}

func (l *lookups) lookup(_ context.Context, team string) (storagequota.Standing, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.fail {
		return storagequota.Standing{}, errors.New("controller unreachable")
	}
	return l.answers[team], nil
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newQuota(used *counts, l *lookups, c *clock) *storagequota.Quota {
	return storagequota.New(storagequota.Options{
		Share:  func(allowance int64) int64 { return allowance },
		Used:   used.get,
		Lookup: l.lookup,
		Exempt: func(team string) bool { return team == "" },
		Now:    c.Now,
	})
}

func free(allowance int64) storagequota.Standing {
	return storagequota.Standing{Tier: storagequota.TierFree, AllowanceBytes: allowance}
}

// Two writes in flight see each other's reservations, a refused or failed
// write holds nothing, and what a write stored is the store's count from
// then on.
func TestReserveCountsWritesInFlight(t *testing.T) {
	used := &counts{used: map[string]int64{"acme": 60}}
	q := newQuota(used, &lookups{answers: map[string]storagequota.Standing{"acme": free(100)}}, &clock{now: time.Now()})
	ctx := context.Background()

	_, err := q.Reserve(ctx, "acme", 41)
	var refused *storagequota.Error
	if !errors.As(err, &refused) || refused.Used != 60 || refused.Allowed != 100 || !strings.Contains(err.Error(), "add credits") {
		t.Fatalf("41 past 60 of 100 = %v, want a refusal naming the share", err)
	}
	first, err := q.Reserve(ctx, "acme", 30)
	if err != nil {
		t.Fatalf("30 of the 40 left: %v", err)
	}
	if _, err := q.Reserve(ctx, "acme", 20); err == nil {
		t.Fatal("20 more while 30 are in flight was admitted; the reservation was not seen")
	}
	first()
	failed, err := q.Reserve(ctx, "acme", 40)
	if err != nil {
		t.Fatalf("40 after a write that stored nothing released: %v", err)
	}
	failed()
	written, err := q.Reserve(ctx, "acme", 40)
	if err != nil {
		t.Fatal(err)
	}
	used.add("acme", 40)
	written()
	if _, err := q.Reserve(ctx, "acme", 1); err == nil {
		t.Fatal("a byte past a full share was admitted")
	}
	if got := q.FreeUsedBytes(); got != 100 {
		t.Fatalf("free used = %d, want acme's 100", got)
	}
}

func TestReserveUpToGrantsTheRoomLeft(t *testing.T) {
	used := &counts{used: map[string]int64{"acme": 60}}
	q := newQuota(used, &lookups{answers: map[string]storagequota.Standing{"acme": free(100)}}, &clock{now: time.Now()})
	ctx := context.Background()
	granted, release, err := q.ReserveUpTo(ctx, "acme", 1000)
	if err != nil || granted != 40 {
		t.Fatalf("up to 1000 with 40 left = %d, %v", granted, err)
	}
	if _, _, err := q.ReserveUpTo(ctx, "acme", 1); err == nil {
		t.Fatal("a full share granted room")
	}
	release()
	if granted, _, err := q.ReserveUpTo(ctx, "acme", 10); err != nil || granted != 10 {
		t.Fatalf("up to 10 = %d, %v", granted, err)
	}
}

// A failed lookup keeps the last answer and a team never answered for is
// free, so an outage lifts no limit; a funded team and the operator are
// unlimited, and a team with no slot stores nothing.
func TestAFailedLookupNeverLiftsALimit(t *testing.T) {
	used := &counts{used: map[string]int64{}}
	l := &lookups{answers: map[string]storagequota.Standing{
		"paying": {Tier: storagequota.TierFunded, AllowanceBytes: 100},
		"none":   {Tier: storagequota.TierNone, AllowanceBytes: 100},
		"free":   free(100),
	}}
	c := &clock{now: time.Now()}
	q := newQuota(used, l, c)
	ctx := context.Background()
	big := int64(1 << 40)
	if _, err := q.Reserve(ctx, "paying", big); err != nil {
		t.Fatalf("a funded team: %v", err)
	}
	if _, err := q.Reserve(ctx, "", big); err != nil {
		t.Fatalf("the operator: %v", err)
	}
	if _, err := q.Reserve(ctx, "none", 1); !errors.Is(err, storagequota.ErrPaused) {
		t.Fatalf("a team with no slot = %v, want ErrPaused", err)
	}
	if _, err := q.Reserve(ctx, "free", 101); err == nil {
		t.Fatal("a free team past its share was admitted")
	}

	l.mu.Lock()
	l.fail = true
	l.answers["free"] = storagequota.Standing{Tier: storagequota.TierFunded}
	l.mu.Unlock()
	c.now = c.now.Add(2 * storagequota.DefaultStandingTTL)
	if _, err := q.Reserve(ctx, "free", 101); err == nil {
		t.Fatal("a failed lookup lifted a free team's limit")
	}
	if _, err := q.Reserve(ctx, "never-seen", storagequota.DefaultAllowanceBytes+1); err == nil {
		t.Fatal("a team never answered for was admitted past the default allowance")
	}
	if _, err := q.Reserve(ctx, "never-seen", 1); err != nil {
		t.Fatalf("a team never answered for, inside the default allowance: %v", err)
	}
}

func TestAStandingIsAskedForOncePerTTL(t *testing.T) {
	l := &lookups{answers: map[string]storagequota.Standing{"acme": free(100)}}
	c := &clock{now: time.Now()}
	q := newQuota(&counts{used: map[string]int64{}}, l, c)
	for range 3 {
		release, err := q.Reserve(context.Background(), "acme", 1)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	c.now = c.now.Add(storagequota.DefaultStandingTTL)
	if _, err := q.Reserve(context.Background(), "acme", 1); err != nil {
		t.Fatal(err)
	}
	if l.calls != 2 {
		t.Fatalf("lookups = %d, want one per minute", l.calls)
	}
}

func TestHTTPLookupReadsTheControllersTierRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/teams/acme/storage-tier" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"team":"acme","tier":"free","allowance_bytes":1024}`))
	}))
	t.Cleanup(srv.Close)
	got, err := storagequota.HTTPLookup(srv.URL, "tok", nil)(context.Background(), "acme")
	if err != nil || got != free(1024) {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	if _, err := storagequota.HTTPLookup(srv.URL, "wrong", nil)(context.Background(), "acme"); err == nil {
		t.Fatal("a refused lookup read as an answer")
	}
}

func TestSharesSplitTheAllowance(t *testing.T) {
	a := storagequota.DefaultAllowanceBytes
	if storagequota.CacheShare(a) != 768<<20 || storagequota.LogShare(a) != 192<<20 || storagequota.EventShare(a) != 64<<20 {
		t.Fatalf("shares of 1 GiB = %d, %d, %d", storagequota.CacheShare(a), storagequota.LogShare(a), storagequota.EventShare(a))
	}
}

// A funded answer outlives a controller outage only briefly: once it is five
// minutes old and a refresh fails, the team is held to the free share. A team
// the controller last answered none stays refused.
func TestAStaleFundedStandingFallsBackToFreeDuringAnOutage(t *testing.T) {
	used := &counts{used: map[string]int64{}}
	l := &lookups{answers: map[string]storagequota.Standing{
		"paying": {Tier: storagequota.TierFunded, AllowanceBytes: 100},
		"none":   {Tier: storagequota.TierNone, AllowanceBytes: 100},
	}}
	c := &clock{now: time.Now()}
	q := newQuota(used, l, c)
	ctx := context.Background()
	for _, team := range []string{"paying", "none"} {
		if release, err := q.Reserve(ctx, team, 1); err == nil {
			release()
		}
	}
	l.mu.Lock()
	l.fail = true
	l.mu.Unlock()

	c.now = c.now.Add(2 * time.Minute)
	if _, err := q.Reserve(ctx, "paying", 1000); err != nil {
		t.Fatalf("a funded team two minutes into an outage: %v", err)
	}
	c.now = c.now.Add(4 * time.Minute)
	if _, err := q.Reserve(ctx, "paying", 1000); err == nil {
		t.Fatal("a funded standing six minutes stale still lifted the limit")
	}
	if _, err := q.Reserve(ctx, "paying", 100); err != nil {
		t.Fatalf("a stale funded team inside the free share: %v", err)
	}
	if _, err := q.Reserve(ctx, "none", 1); !errors.Is(err, storagequota.ErrPaused) {
		t.Fatalf("a team last answered none, during the outage = %v, want ErrPaused", err)
	}
}
