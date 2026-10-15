package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A freeze is one row per dispute and the team stays frozen while any is
// unreleased: releasing one dispute leaves the other's hold, and holding a
// dispute again, however often it is replayed, never undoes a release.
func TestATeamStaysFrozenWhileAnyDisputeHoldsIt(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	now := time.Now()
	for _, dispute := range []string{"dp_1", "dp_2", "dp_1"} {
		if _, err := acme.HoldForDispute(ctx, dispute, "", "dispute opened", now); err != nil {
			t.Fatalf("hold %s: %v", dispute, err)
		}
	}
	frozen := func() store.TeamCreditFreeze {
		t.Helper()
		f, err := acme.CreditFreeze(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := frozen(); !f.Frozen || len(f.Disputes) != 2 {
		t.Fatalf("freeze = %+v, want two disputes holding the team", f)
	}
	if n, err := acme.ReleaseCreditFreezes(ctx, "dp_1", "", "", now); err != nil || n != 1 {
		t.Fatalf("release dp_1 = %d, %v", n, err)
	}
	if f := frozen(); !f.Frozen || len(f.Disputes) != 1 || f.Disputes[0] != "dp_2" {
		t.Fatalf("freeze after releasing one dispute = %+v, want dp_2 still holding it", f)
	}
	if created, err := acme.HoldForDispute(ctx, "dp_1", "", "replayed", now); err != nil || created {
		t.Fatalf("a replayed hold of a released dispute = %v, %v; want nothing written", created, err)
	}
	if f := frozen(); len(f.Disputes) != 1 {
		t.Fatalf("a replay re-held a released dispute: %+v", f)
	}
	if n, err := acme.ReleaseCreditFreezes(ctx, "dp_2", "", "", now); err != nil || n != 1 {
		t.Fatalf("release dp_2 = %d, %v", n, err)
	}
	if f := frozen(); f.Frozen {
		t.Fatalf("freeze after every release = %+v", f)
	}
}

// A release names its hold: an empty or blank id is refused and releases
// nothing, so it can never stand for every hold on the team.
func TestReleaseCreditFreezesRefusesABlankHold(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, s, "acme")
	now := time.Now()
	if _, err := acme.HoldForDispute(ctx, "dp_1", "", "dispute opened", now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "   "} {
		if n, err := acme.ReleaseCreditFreezes(ctx, id, "op", "resolved", now); !errors.Is(err, store.ErrInvalidInput) || n != 0 {
			t.Errorf("release %q = %d, %v; want an invalid-input refusal", id, n, err)
		}
	}
	if f, err := acme.CreditFreeze(ctx); err != nil || !f.Frozen || len(f.Disputes) != 1 {
		t.Fatalf("freeze after the refusals = %+v, %v; want dp_1 still holding", f, err)
	}
}
