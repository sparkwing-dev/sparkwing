package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runretry"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A manual retry is asked for by run id, so it belongs to the source run's
// team. Filed under the default team it is invisible to the team that asked
// and claimable, repository URL and all, by the default team's runners.
func TestManualRetryStaysInTheSourceRunsTeam(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t).Open(t)
	acme := tenantFor(t, st, "acme")
	now := time.Now()
	if err := acme.CreateRun(ctx, store.Run{
		ID: "run-src", Pipeline: "deploy", Status: "failed", StartedAt: now,
		RepoURL: "https://example.com/acme/private.git",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runretry.Create(ctx, st, "run-src", "run-retry", false, now); err != nil {
		t.Fatalf("runretry.Create: %v", err)
	}
	if got := storedTeam(t, st, `SELECT team FROM triggers WHERE id = ?`, "run-retry"); got != "acme" {
		t.Errorf("retry trigger of an acme run carries team %q, want acme", got)
	}
	if got := storedTeam(t, st, `SELECT team FROM runs WHERE id = ?`, "run-retry"); got != "acme" {
		t.Errorf("retry run of an acme run carries team %q, want acme", got)
	}

	home := mintClaimant(t, st, "agent:default-laptop")
	if trig, err := st.ClaimNextTriggerFor(ctx, home, time.Minute, nil, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a default-team runner claimed %+v (err %v), want not found", trig, err)
	}
	own := mintTeamClaimant(t, acme, "agent:acme")
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-retry", own, time.Minute); err != nil {
		t.Fatalf("acme's runner claiming its own retry: %v", err)
	}
}

func TestCreateRetryWithRunRefusesAMissingSource(t *testing.T) {
	st := storetest.New(t).Open(t)
	now := time.Now()
	err := st.CreateRetryWithRun(context.Background(), "run-gone",
		store.Trigger{ID: "run-retry", Pipeline: "deploy", CreatedAt: now},
		store.Run{ID: "run-retry", Pipeline: "deploy", Status: "pending", StartedAt: now})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateRetryWithRun of a missing source = %v, want not found", err)
	}
}
