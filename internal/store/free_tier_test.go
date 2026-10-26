package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func setFreeAllowance(t *testing.T, st *store.Store, bytes int64) {
	t.Helper()
	if _, err := st.AsOperator().SetCreditSettings(context.Background(), store.CreditSettingsUpdate{
		StorageFreeAllowanceBytes: &bytes,
	}); err != nil {
		t.Fatalf("set the free allowance: %v", err)
	}
}

func startRun(tenant *store.Tenant, id string) error {
	return tenant.TestOnlySeedTrigger(context.Background(), store.Trigger{ID: id, Pipeline: "p", CreatedAt: time.Now()})
}

func fund(t *testing.T, tenant *store.Tenant) {
	t.Helper()
	if _, err := tenant.TestOnlySettlePaidCredit(context.Background(), store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: store.MicroCreditsPerCent, Reference: "pay-" + string(tenant.Team()), CreatedBy: "admin"}); err != nil {
		t.Fatalf("fund %s: %v", tenant.Team(), err)
	}
}

func standing(t *testing.T, st *store.Store, team store.Team) store.StorageStanding {
	t.Helper()
	got, err := tenant(t, st, team).StorageStanding(context.Background())
	if err != nil {
		t.Fatalf("standing of %s: %v", team, err)
	}
	return got
}

// A run is never refused for billing: a team without credits or a slot, one
// past any daily count, and one held over a disputed payment all start runs,
// which their own machines execute.
func TestTriggersAreNeverRefusedForBilling(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if _, err := st.AsOperator().ConfigureDeployment(ctx, "", 0, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	acme := teamHandle(t, st, "acme")
	for i := range 201 {
		if err := startRun(acme, fmt.Sprintf("t-%d", i)); err != nil {
			t.Fatalf("run %d of a team with no credits and no slot: %v", i, err)
		}
	}
	if taken, _, err := st.AsOperator().FreeSlots(ctx); err != nil || taken != 0 {
		t.Fatalf("slots taken = %d, %v; want a run to take none", taken, err)
	}
	held := teamHandle(t, st, "held")
	fund(t, held)
	if _, err := teststore.ForTeam(t, st, "held").TestOnlyHoldForDispute(ctx, "dp_1", "pay-held", "dispute", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := startRun(held, "t-held"); err != nil {
		t.Fatalf("a run of a team held over a dispute: %v", err)
	}
}

func appendBytes(tenant *store.Tenant, runID string, n int) error {
	_, err := tenant.AppendEventCharged(context.Background(), runID, "", "k",
		[]byte(strings.Repeat("x", n-1)))
	return err
}

func teamRun(t *testing.T, tenant *store.Tenant, runID string) {
	t.Helper()
	if err := tenant.TestOnlySeedRun(context.Background(), store.Run{RunSummary: runstate.RunSummary{ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now()}}); err != nil {
		t.Fatalf("create run %s for %s: %v", runID, tenant.Team(), err)
	}
}

// The controller holds a free team's run events to one sixteenth of the
// allowance, counted in the append's own transaction.
func TestAFreeTeamsEventsStopAtTheirShare(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 1600)
	acme := teamHandle(t, st, "acme")
	teamRun(t, acme, "r1")
	if err := appendBytes(acme, "r1", 60); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if got := standing(t, st, "acme"); got.Tier != "free" || got.EventBytes != 60 {
		t.Fatalf("after the first write = %+v, want a slot holding 60 bytes", got)
	}
	err := appendBytes(acme, "r1", 41)
	var quota *store.StorageQuotaError
	if !errors.As(err, &quota) || quota.Limit != "free_event_share" || !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("a write past the share = %v, want a free event share refusal", err)
	}
	if !strings.Contains(err.Error(), "60 of 100 bytes") || !strings.Contains(err.Error(), "add credits") {
		t.Fatalf("refusal = %q, want it to name the share and the remedy", err)
	}
	if err := appendBytes(acme, "r1", 40); err != nil {
		t.Fatalf("a write that fills the share exactly: %v", err)
	}
	fund(t, acme)
	if err := appendBytes(acme, "r1", 500); err != nil {
		t.Fatalf("a funded team's write past the share: %v", err)
	}
	if got := standing(t, st, "acme"); got.EventBytes != 600 {
		t.Fatalf("event bytes = %d, want every admitted byte counted", got.EventBytes)
	}
}

// Negative control: the operator's own team has no free tier, so the same
// write that refuses a signed-up team is admitted for it and takes no slot.
func TestTheOperatorsTeamIsNotHeldToAnEventShare(t *testing.T) {
	st := storetest.Open(t)
	setFreeAllowance(t, st, 1600)
	seedRunWithNode(t, st, "r1", "n1", "running")
	if err := appendBytes(storageTestTenant(t, st), "r1", 500); err != nil {
		t.Fatalf("an operator write past the share: %v", err)
	}
	if taken, _, err := st.AsOperator().FreeSlots(context.Background()); err != nil || taken != 0 {
		t.Fatalf("slots taken = %d, %v; want none", taken, err)
	}
}

func TestExpiryAndDeletionLowerAFreeTeamsEventBytes(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	for _, id := range []string{"old", "gone", "new"} {
		teamRun(t, acme, id)
		if err := appendBytes(acme, id, 100); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE runs SET status = 'success', finished_at = ? WHERE id = 'old'`), now.Add(-31*24*time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := st.AsOperator().SetStorageSettings(ctx, store.StorageSettings{RunRetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AsOperator().MaintainRetention(ctx, store.RetentionRequest{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, "acme"); got.EventBytes != 200 {
		t.Fatalf("after expiry = %d, want the two live runs' 200", got.EventBytes)
	}
	if err := acme.RemoveRunForTest(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, "acme"); got.EventBytes != 100 {
		t.Fatalf("after deletion = %d, want the one run left", got.EventBytes)
	}
}
