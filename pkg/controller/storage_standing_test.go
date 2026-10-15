package controller_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func freeTierFixture(t *testing.T, slots int64) *identityFixture {
	t.Helper()
	raw, pub := multiTeamLicense(t)
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub})
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

// A sign-up gate reads the free tier closed once the last slot is taken, and
// an error, never open, when the slots cannot be read.
func TestSignUpFreeTierClosesWithTheLastSlot(t *testing.T) {
	f := freeTierFixture(t, 1)
	ctx := context.Background()
	if got, err := f.srv.SignUpFreeTier(ctx); err != nil || got != controller.FreeTierOpen {
		t.Fatalf("with a slot free = %s, %v; want open", got, err)
	}
	freeTeamToken(t, f.store, "first")
	res, err := f.store.ReserveStorage(ctx, store.StorageReserve{Team: "first", Kind: store.StorageCache, Bytes: 1, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CommitStorage(ctx, store.StorageCommit{ID: res.ID, Team: "first", Kind: store.StorageCache, Bytes: 1, Now: time.Now()}); err != nil {
		t.Fatal(err)
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
