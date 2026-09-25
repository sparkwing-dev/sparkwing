package controller_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func freeTierFixture(t *testing.T, slots int64) *identityFixture {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub, configure: func(s *controller.Server) {
		s.WithCacheCredentials("http://cache.invalid", "cache-token")
	}})
	if err := f.store.SetFreeTeamSlots(context.Background(), slots); err != nil {
		t.Fatal(err)
	}
	return f
}

func freeTeamToken(t *testing.T, st *store.Store, team store.Team) string {
	t.Helper()
	ctx := context.Background()
	if err := st.AsOperator().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	tenant, err := st.ForTeam(ctx, team)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := tenant.CreateToken(ctx, "ci", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite, controller.ScopeRunsRead}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + raw
}

func (f *identityFixture) trigger(auth string) int {
	f.t.Helper()
	return f.call("POST", "/api/v1/triggers", auth, map[string]any{
		"pipeline": "build", "trigger": map[string]any{"source": "api"},
	}, nil)
}

func (f *identityFixture) reserve(auth, team, kind string, bytes int64) (int, store.StorageReservation) {
	f.t.Helper()
	var out store.StorageReservation
	code := f.call("POST", "/internal/storage/reserve", auth, map[string]any{"team": team, "store": kind, "bytes": bytes}, &out)
	return code, out
}

func logsWriter(t *testing.T, st *store.Store, team store.Team) string {
	t.Helper()
	tenant, err := st.ForTeam(context.Background(), team)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := tenant.CreateToken(context.Background(), "runner", store.TokenKindUser,
		[]string{controller.ScopeLogsWrite}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + raw
}

func TestLogsWriterCommitRequiresReservationAndNonnegativeBytes(t *testing.T) {
	f := freeTierFixture(t, 1)
	teamAuth := freeTeamToken(t, f.store, "first")
	if code := f.trigger(teamAuth); code != http.StatusAccepted {
		t.Fatalf("trigger = %d", code)
	}
	writer := logsWriter(t, f.store, "first")
	code, reservation := f.reserve(writer, "first", "logs", 100)
	if code != http.StatusOK {
		t.Fatalf("reserve = %d", code)
	}
	for _, req := range []map[string]any{
		{"team": "first", "store": "logs", "reservation": "", "bytes": -100},
		{"team": "first", "store": "logs", "reservation": "", "bytes": 100},
		{"team": "first", "store": "logs", "reservation": reservation.ID, "bytes": -1},
		{"team": "first", "store": "logs", "reservation": reservation.ID, "bytes": -1, "next_bytes": 1},
	} {
		if code := f.call("POST", "/internal/storage/commit", writer, req, nil); code != http.StatusBadRequest {
			t.Fatalf("invalid logs commit %+v = %d", req, code)
		}
	}
	if code := f.call("POST", "/internal/storage/commit", writer, map[string]any{
		"team": "first", "store": "logs", "reservation": reservation.ID, "bytes": 100,
	}, nil); code != http.StatusNoContent {
		t.Fatalf("valid logs commit = %d", code)
	}
	if usage, err := f.store.TeamStorage(context.Background(), "first"); err != nil || usage[store.StorageLogs].UsedBytes != 100 {
		t.Fatalf("logs usage = %+v, err = %v", usage, err)
	}
	const cache = "Bearer cache-token"
	if code := f.call("POST", "/internal/storage/commit", cache, map[string]any{
		"team": "first", "store": "cache", "bytes": -1,
	}, nil); code != http.StatusNoContent {
		t.Fatalf("cache service negative delta = %d", code)
	}
}

// The cache reserves, commits and releases a team's storage with its
// operator token. A team that took the last slot is held to its cache
// share, the next team is refused as paused until the operator grants it a
// slot, and the operator's own team is never held.
func TestTheCounterRoutesHoldAFreeTeamToItsShare(t *testing.T) {
	f := freeTierFixture(t, 1)
	allowance := int64(4096)
	if _, err := f.store.SetCreditSettings(context.Background(), store.CreditSettingsUpdate{
		StorageFreeAllowanceBytes: &allowance,
	}); err != nil {
		t.Fatal(err)
	}
	first := freeTeamToken(t, f.store, "first")
	second := freeTeamToken(t, f.store, "second")
	if code := f.trigger(first); code != http.StatusAccepted {
		t.Fatalf("first team's run = %d", code)
	}
	if code := f.trigger(second); code != http.StatusPaymentRequired {
		t.Fatalf("second team's run with no slot = %d, want 402", code)
	}
	const cache = "Bearer cache-token"
	code, res := f.reserve(cache, "first", "cache", 3072)
	if code != http.StatusOK || res.ID == "" || res.Tier != store.TeamTierFree {
		t.Fatalf("reserve the whole cache share = %d %+v", code, res)
	}
	if code, _ := f.reserve(cache, "first", "cache", 1); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a byte past the share = %d, want 413", code)
	}
	if code := f.call("POST", "/internal/storage/commit", cache, map[string]any{
		"team": "first", "store": "cache", "reservation": res.ID, "bytes": 3000,
	}, nil); code != http.StatusNoContent {
		t.Fatalf("commit = %d", code)
	}
	code, res = f.reserve(cache, "first", "cache", 72)
	if code != http.StatusOK {
		t.Fatalf("the 72 bytes the commit left = %d", code)
	}
	if code := f.call("POST", "/internal/storage/release", cache, map[string]any{
		"team": "first", "reservation": res.ID,
	}, nil); code != http.StatusNoContent {
		t.Fatalf("release = %d", code)
	}
	code, res = f.reserve(cache, "first", "cache", 72)
	if code != http.StatusOK {
		t.Fatalf("the 72 bytes a release gave back = %d", code)
	}
	var next store.StorageReservation
	if code := f.call("POST", "/internal/storage/commit", cache, map[string]any{
		"team": "first", "store": "cache", "reservation": res.ID, "bytes": 40, "next_bytes": 100,
	}, &next); code != http.StatusOK || next.ID == "" || next.Granted != 32 {
		t.Fatalf("commit 40 and take up to 100 more = %d %+v, want the 32 bytes left as the next block", code, next)
	}
	if code := f.call("POST", "/internal/storage/release", cache, map[string]any{
		"team": "first", "reservation": next.ID,
	}, nil); code != http.StatusNoContent {
		t.Fatalf("release the next block = %d", code)
	}
	if code, res = f.reserve(cache, "first", "cache", 32); code != http.StatusOK {
		t.Fatalf("the 32 bytes left after the renew = %d", code)
	}
	_ = res
	if code, _ := f.reserve(cache, "second", "cache", 1); code != http.StatusPaymentRequired {
		t.Fatalf("a team with no slot = %d, want 402", code)
	}
	if code, res := f.reserve(cache, "default", "cache", 1<<40); code != http.StatusOK || res.Tier != store.TeamTierFunded {
		t.Fatalf("the operator's team = %d %+v, want funded", code, res)
	}
	if code := f.call("PUT", "/api/v1/storage/teams/second/free-slot", "Bearer "+f.admin, nil, nil); code != http.StatusOK {
		t.Fatalf("operator slot grant = %d", code)
	}
	if code, _ := f.reserve(cache, "second", "cache", 1); code != http.StatusOK {
		t.Fatalf("a granted team = %d, want 200", code)
	}
}

// A credential the logs service forwards counts only its own team's logs;
// only the cache's token names any team, charges downloads or records
// egress, and nothing else reaches the routes.
func TestTheCounterRoutesAnswerOnlyTheirCallers(t *testing.T) {
	f := freeTierFixture(t, 5)
	for _, team := range []store.Team{"first", "second"} {
		if code := f.trigger(freeTeamToken(t, f.store, team)); code != http.StatusAccepted {
			t.Fatalf("%s's run = %d", team, code)
		}
	}
	writer := logsWriter(t, f.store, "first")
	if code, _ := f.reserve(writer, "first", "logs", 10); code != http.StatusOK {
		t.Fatalf("a logs writer reserving its own team's logs = %d", code)
	}
	for _, c := range []struct{ team, kind string }{{"second", "logs"}, {"first", "cache"}} {
		if code, _ := f.reserve(writer, c.team, c.kind, 10); code != http.StatusForbidden {
			t.Errorf("a logs writer reserving %s's %s = %d, want 403", c.team, c.kind, code)
		}
	}
	if code := f.call("POST", "/internal/downloads/charge", writer, map[string]any{"team": "first", "bytes": 1}, nil); code != http.StatusForbidden {
		t.Errorf("a logs writer charging a download = %d, want 403", code)
	}
	if code := f.call("POST", "/internal/egress/totals", writer, map[string]any{"service": "cache", "day": "2026-09-23", "month": "2026-09"}, nil); code != http.StatusForbidden {
		t.Errorf("a logs writer recording egress = %d, want 403", code)
	}
	if code, _ := f.reserve(freeTeamToken(t, f.store, "third"), "third", "logs", 10); code != http.StatusUnauthorized {
		t.Errorf("a token without logs.write = %d, want 401", code)
	}
	if code, _ := f.reserve("", "first", "logs", 10); code != http.StatusUnauthorized {
		t.Errorf("no bearer = %d, want 401", code)
	}
	if code, _ := f.reserve("Bearer cache-token", "Not A Team", "cache", 10); code != http.StatusBadRequest {
		t.Errorf("a team that is not a slug = %d, want 400", code)
	}
}

// Readers racing for the last bytes of a team's download day each see room
// alone; the controller admits exactly one and tells the rest when to retry.
func TestTheDownloadChargeRouteHoldsTheDailyCap(t *testing.T) {
	f := freeTierFixture(t, 5)
	f.srv.WithTeamDownloadCaps(100, 1000)
	if code := f.trigger(freeTeamToken(t, f.store, "first")); code != http.StatusAccepted {
		t.Fatal(code)
	}
	charge := func(bytes int64) (int, http.Header) {
		req, err := http.NewRequest(http.MethodPost, f.url+"/internal/downloads/charge",
			strings.NewReader(fmt.Sprintf(`{"team":"first","bytes":%d}`, bytes)))
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		req.Header.Set("Authorization", "Bearer cache-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode, resp.Header
	}
	if code, _ := charge(40); code != http.StatusOK {
		t.Fatalf("40 of 100 = %d", code)
	}
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = charge(60)
		}()
	}
	wg.Wait()
	won := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			won++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("a racing charge = %d", c)
		}
	}
	if won != 1 {
		t.Fatalf("%d racing charges took the last 60 bytes, want exactly 1", won)
	}
	code, h := charge(1)
	retry, err := strconv.Atoi(h.Get("Retry-After"))
	if code != http.StatusTooManyRequests || err != nil || retry < 1 || retry > 86400 {
		t.Fatalf("a charge past the cap = %d, Retry-After %q; want 429 and the wait until midnight UTC", code, h.Get("Retry-After"))
	}
}

// Negative control: a single-team controller has no free tier, so every
// team reserves funded and nothing is refused.
func TestASingleTeamControllerCountsNoShare(t *testing.T) {
	f := newIdentityFixtureWith(t, fixtureOpts{configure: func(s *controller.Server) {
		s.WithCacheCredentials("http://cache.invalid", "cache-token")
	}})
	if err := f.store.AsOperator().CreateTeam(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if code, got := f.reserve("Bearer cache-token", "acme", "cache", 1<<40); code != http.StatusOK || got.Tier != store.TeamTierFunded {
		t.Fatalf("reserve = %d %+v, want funded", code, got)
	}
}

// A sign-up gate reads the free tier closed once the last slot is taken, and
// an error, never open, when the slots cannot be read.
func TestSignUpFreeTierClosesWithTheLastSlot(t *testing.T) {
	f := freeTierFixture(t, 1)
	ctx := context.Background()
	if got, err := f.srv.SignUpFreeTier(ctx); err != nil || got != controller.FreeTierOpen {
		t.Fatalf("with a slot free = %s, %v; want open", got, err)
	}
	if code := f.trigger(freeTeamToken(t, f.store, "first")); code != http.StatusAccepted {
		t.Fatalf("first team's run = %d", code)
	}
	if got, err := f.srv.SignUpFreeTier(ctx); err != nil || got != controller.FreeTierClosed {
		t.Fatalf("with every slot taken = %s, %v; want closed", got, err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := f.srv.SignUpFreeTier(ctx); err == nil || got == controller.FreeTierOpen {
		t.Fatalf("with the store unreadable = %s, %v; want an error and not open", got, err)
	}
}

// The controller wires its own slot count as the sign-up gate's free tier, so
// a multi-team controller logs nothing about an unwired source and waitlists
// a new account once the last free slot is taken.
func TestTheServerIsItsOwnFreeTierSource(t *testing.T) {
	raw, pub := multiTeamLicense(t)
	var buf syncBuffer
	f := newIdentityFixtureWith(t, fixtureOpts{
		license: raw, key: pub, logger: slog.New(slog.NewJSONHandler(&buf, nil)),
		configure: func(s *controller.Server) {
			s.WithFreeTier(s)
			s.CheckFreeTierSource()
		},
	})
	if strings.Contains(buf.String(), "signup.free_tier_unwired") {
		t.Fatalf("a server wired to itself logged an unwired free tier:\n%s", buf.String())
	}
	if err := f.store.SetFreeTeamSlots(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if out := f.signIn(person("g-full", "full@example.com", "F")); !out.Waitlisted {
		t.Fatal("a new account was admitted with every free slot taken")
	}
}
