package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A runner places a run's Jobs by the team a claim and a read return, so
// both must report the trigger's own team rather than an empty one.
func TestTrigger_ClaimAndReadReturnTheOwningTeam(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()

	acme := teamHandle(t, s, "acme")
	if err := acme.CreateTrigger(ctx, store.Trigger{ID: "t-acme", Pipeline: "demo", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	// Control: the unauthenticated local claim is the default team's, so it
	// leaves acme's trigger alone.
	if tr, err := s.ClaimNextTriggerFor(ctx, store.ClaimIdentity{}, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("default-team claim = %+v, %v; want nothing", tr, err)
	}

	_, tok, err := acme.CreateToken(ctx, "acme-runner", store.TokenKindRunner, []string{"triggers.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	claimed, err := s.ClaimNextTriggerFor(ctx,
		store.ClaimIdentity{Principal: "acme-runner", TokenPrefix: tok.Prefix}, time.Minute, nil, nil)
	if err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	if claimed.ID != "t-acme" || claimed.Team != "acme" {
		t.Fatalf("claimed %s team = %q, want t-acme in acme", claimed.ID, claimed.Team)
	}
	read, err := s.GetTrigger(ctx, "t-acme")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if read.Team != "acme" {
		t.Fatalf("read team = %q, want acme", read.Team)
	}

	seedPending(t, s, "t-legacy")
	legacy, err := s.GetTrigger(ctx, "t-legacy")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if legacy.Team != store.DefaultTeam {
		t.Fatalf("unscoped trigger team = %q, want %q", legacy.Team, store.DefaultTeam)
	}
}
