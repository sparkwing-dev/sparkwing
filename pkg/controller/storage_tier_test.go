package controller_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
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

func (f *identityFixture) tier(team, auth string) (int, controller.StorageTierResponse) {
	f.t.Helper()
	var out controller.StorageTierResponse
	code := f.call("GET", "/internal/teams/"+team+"/storage-tier", auth, nil, &out)
	return code, out
}

// The cache reads a team's tier with its operator token, a team that took
// the last slot reads free, and the next team is refused its first run with
// 402 and reads none until the operator grants it a slot.
func TestTheStorageTierRouteFollowsTheSlots(t *testing.T) {
	f := freeTierFixture(t, 1)
	first := freeTeamToken(t, f.store, "first")
	second := freeTeamToken(t, f.store, "second")

	if code := f.trigger(first); code != http.StatusAccepted {
		t.Fatalf("first team's run = %d", code)
	}
	if code := f.trigger(second); code != http.StatusPaymentRequired {
		t.Fatalf("second team's run with no slot = %d, want 402", code)
	}
	for team, want := range map[string]storagequota.Tier{
		"first": storagequota.TierFree, "second": storagequota.TierNone, "default": storagequota.TierFunded,
	} {
		code, got := f.tier(team, "Bearer cache-token")
		if code != http.StatusOK || got.Tier != want || got.AllowanceBytes != storagequota.DefaultAllowanceBytes {
			t.Errorf("tier of %s = %d %+v, want %s", team, code, got, want)
		}
	}
	if code, _ := f.tier("first", first); code != http.StatusUnauthorized {
		t.Fatalf("a team token reading the tier route = %d, want 401", code)
	}
	if code := f.call("PUT", "/api/v1/storage/teams/second/free-slot", "Bearer "+f.admin, nil, nil); code != http.StatusOK {
		t.Fatalf("operator slot grant = %d", code)
	}
	if code := f.trigger(second); code != http.StatusAccepted {
		t.Fatalf("second team's run after the grant = %d", code)
	}
}

// Negative control: a single-team controller has no free tier, so every
// team reads funded and nothing takes a slot.
func TestASingleTeamControllerAnswersEveryTeamFunded(t *testing.T) {
	f := newIdentityFixtureWith(t, fixtureOpts{configure: func(s *controller.Server) {
		s.WithCacheCredentials("http://cache.invalid", "cache-token")
	}})
	if err := f.store.AsOperator().CreateTeam(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if code, got := f.tier("acme", "Bearer cache-token"); code != http.StatusOK || got.Tier != storagequota.TierFunded {
		t.Fatalf("tier = %d %+v, want funded", code, got)
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
