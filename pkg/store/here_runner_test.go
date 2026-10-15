package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// safety: a credential left unused past the claim window can no longer claim
// its run, while one inside the window still can.
func TestCreateHereTriggerWithRun_RefusesAClaimAfterTheWindow(t *testing.T) {
	st := storetest.Open(t)
	ctx := store.WithoutCreditMetering(context.Background())
	tenant, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claim := func(id string, at time.Time) error {
		t.Helper()
		_, tok, err := tenant.CreateHereTriggerWithRun(ctx,
			store.Trigger{ID: id, Pipeline: "hotfix", TriggerSource: "cli", CreatedAt: at},
			store.Run{ID: id, Pipeline: "hotfix", Status: "pending", CreatedAt: at, StartedAt: at},
			store.HereRunner{Reason: "INC-42", By: "dev@example.com", Scopes: store.MachineTokenScopes(), TTL: 6 * time.Hour}, at)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.ClaimSpecificTriggerFor(ctx, id, store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}, time.Minute)
		return err
	}
	if err := claim("run-late", now.Add(-store.HereRunnerClaimWindow-time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("claim after the window = %v, want refused", err)
	}
	if err := claim("run-in-time", now.Add(-store.HereRunnerClaimWindow+time.Minute)); err != nil {
		t.Fatalf("claim inside the window = %v, want it taken", err)
	}
}
