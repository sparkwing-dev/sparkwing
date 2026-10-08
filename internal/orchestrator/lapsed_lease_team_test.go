package orchestrator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// The local consumer's sweep lists every team's lapsed claims, so a pending
// run of a team other than the default goes back to its queue rather than
// staying claimed by a lease nobody renews.
func TestALapsedLeaseRequeuesAnotherTeamsUnstartedRun(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.AsOperator().CreateTeam(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	acme, err := st.ForTeam(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := acme.CreateTriggerWithRun(ctx,
		store.Trigger{ID: "run-acme", Pipeline: "p", CreatedAt: now},
		store.Run{ID: "run-acme", Pipeline: "p", Status: "pending", CreatedAt: now},
	); err != nil {
		t.Fatal(err)
	}
	_, tok, err := acme.CreateToken(ctx, "agent:acme", store.TokenKindRunner, []string{"triggers.claim"}, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	claimant := store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}
	if _, err := st.ClaimSpecificTriggerFor(ctx, "run-acme", claimant, time.Minute); err != nil {
		t.Fatalf("ClaimSpecificTriggerFor: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE triggers SET lease_expires_at = 1 WHERE id = 'run-acme'`); err != nil {
		t.Fatal(err)
	}

	requeueExpiredClaims(ctx, st, newInFlightSet(), slog.New(slog.DiscardHandler))

	after, err := acme.GetTrigger(ctx, "run-acme")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if after.Status != "pending" {
		t.Fatalf("acme's lapsed claim is %q, want it back in the queue", after.Status)
	}
}
