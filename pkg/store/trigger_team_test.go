package store_test

import (
	"context"
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

	seedPending(t, s, "t-acme")
	if _, err := s.DB().Exec(storetest.Rebind(s,
		`UPDATE triggers SET team = ? WHERE id = ?`), "acme", "t-acme"); err != nil {
		t.Fatalf("set team: %v", err)
	}

	claimed, err := s.ClaimNextTriggerFor(ctx, store.ClaimIdentity{}, time.Minute, nil, nil)
	if err != nil {
		t.Fatalf("ClaimNextTriggerFor: %v", err)
	}
	if claimed.Team != "acme" {
		t.Fatalf("claimed team = %q, want acme", claimed.Team)
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
