package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func setFreeAllowance(t *testing.T, st *store.Store, bytes int64) {
	t.Helper()
	if _, err := st.SetCreditSettings(context.Background(), store.CreditSettingsUpdate{
		StorageFreeAllowanceBytes: &bytes,
	}); err != nil {
		t.Fatalf("set the free allowance: %v", err)
	}
}

func startRun(tenant *store.Tenant, id string) error {
	return tenant.CreateTrigger(context.Background(), store.Trigger{ID: id, Pipeline: "p", CreatedAt: time.Now()})
}

func fund(t *testing.T, tenant *store.Tenant) {
	t.Helper()
	if _, err := tenant.GrantCredits(context.Background(), store.CreditGrantPaid, store.MicroCreditsPerCredit,
		"pay-"+string(tenant.Team()), "admin"); err != nil {
		t.Fatalf("fund %s: %v", tenant.Team(), err)
	}
}

func standing(t *testing.T, st *store.Store, team store.Team) store.StorageStanding {
	t.Helper()
	got, err := st.StorageStandingFor(context.Background(), team)
	if err != nil {
		t.Fatalf("standing of %s: %v", team, err)
	}
	return got
}

// The free tier is full the instant its last slot is taken: no sample, no
// pass, no ceiling on bytes. A team that holds a slot keeps running, a funded
// team needs none, and only deleting a team gives its slot back.
func TestFreeSlotsBoundTheFreeTierAtEveryInstant(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	if err := st.SetFreeTeamSlots(ctx, 2); err != nil {
		t.Fatal(err)
	}
	a, b, c := teamHandle(t, st, "team-a"), teamHandle(t, st, "team-b"), teamHandle(t, st, "team-c")
	if got := standing(t, st, "team-a"); got.Tier != store.TeamTierNone {
		t.Fatalf("a team that never ran = %+v, want no slot yet", got)
	}
	for _, tenant := range []*store.Tenant{a, b} {
		if err := startRun(tenant, "t-"+string(tenant.Team())); err != nil {
			t.Fatalf("%s's first run: %v", tenant.Team(), err)
		}
	}
	err := startRun(c, "t-c")
	if !errors.Is(err, store.ErrFreeStoragePaused) || !strings.Contains(err.Error(), "buy credits or join the waitlist") {
		t.Fatalf("a third team with two slots = %v, want free storage paused", err)
	}
	if err := startRun(a, "t-a-2"); err != nil {
		t.Fatalf("a slotted team's next run: %v", err)
	}
	if got := standing(t, st, "team-a"); got.Tier != store.TeamTierFree || got.AllowanceBytes != store.DefaultFreeAllowanceBytes {
		t.Fatalf("team-a = %+v, want free with the default allowance", got)
	}
	if got := standing(t, st, "team-c"); got.Tier != store.TeamTierNone {
		t.Fatalf("team-c = %+v, want no slot", got)
	}

	d := teamHandle(t, st, "team-d")
	fund(t, d)
	if err := startRun(d, "t-d"); err != nil {
		t.Fatalf("a funded team's run: %v", err)
	}
	if taken, limit, err := st.FreeSlots(ctx); err != nil || taken != 2 || limit != 2 {
		t.Fatalf("slots = %d of %d, %v; want the funded team to take none", taken, limit, err)
	}

	deleteAndPurge(t, st, "team-b", now)
	if err := startRun(c, "t-c-2"); err != nil {
		t.Fatalf("a run after a slotted team was deleted: %v", err)
	}
	if err := st.GrantFreeSlot(ctx, "team-d", now); err != nil {
		t.Fatalf("the operator grants past the cap: %v", err)
	}
	if taken, _, err := st.FreeSlots(ctx); err != nil || taken != 3 {
		t.Fatalf("slots taken = %d, %v; want the operator's grant past the cap", taken, err)
	}
}

// safety: two teams racing for the last slot both read one free; the lock
// admits exactly one of them.
func TestTheLastFreeSlotGoesToOneTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.SetFreeTeamSlots(ctx, 1); err != nil {
		t.Fatal(err)
	}
	const racers = 6
	tenants := make([]*store.Tenant, racers)
	for i := range tenants {
		tenants[i] = teamHandle(t, st, store.Team(fmt.Sprintf("racer-%d", i)))
	}
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i, tenant := range tenants {
		wg.Go(func() { errs[i] = startRun(tenant, fmt.Sprintf("t-%d", i)) })
	}
	wg.Wait()
	admitted := 0
	for _, err := range errs {
		switch {
		case err == nil:
			admitted++
		case !errors.Is(err, store.ErrFreeStoragePaused):
			t.Fatalf("unexpected refusal: %v", err)
		}
	}
	if taken, _, err := st.FreeSlots(ctx); err != nil || taken != 1 || admitted != 1 {
		t.Fatalf("admitted %d, slots taken %d, %v; want exactly one", admitted, taken, err)
	}
}

func TestATeamWithoutCreditsStartsAtMostTwoHundredRunsADay(t *testing.T) {
	st := storetest.Open(t)
	acme := teamHandle(t, st, "acme")
	for i := range store.MaxFreeRunsPerDay {
		if err := startRun(acme, fmt.Sprintf("t-%d", i)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	err := startRun(acme, "t-over")
	if !errors.Is(err, store.ErrFreeRunLimit) || !strings.Contains(err.Error(), "add credits") {
		t.Fatalf("run past the daily cap = %v, want ErrFreeRunLimit naming the remedy", err)
	}
	if _, err := st.DB().Exec(storetest.Rebind(st, `UPDATE triggers SET created_at = ? WHERE id = 't-0'`),
		time.Now().Add(-25*time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := startRun(acme, "t-next-day"); err != nil {
		t.Fatalf("a run once one aged out of the day: %v", err)
	}
	fund(t, acme)
	if err := startRun(acme, "t-funded"); err != nil {
		t.Fatalf("a funded team's run past the cap: %v", err)
	}
}

func appendBytes(st *store.Store, principal, runID string, n int) error {
	_, err := st.AppendEventCharged(context.Background(), principal, runID, "", "k",
		[]byte(strings.Repeat("x", n-1)))
	return err
}

func teamRun(t *testing.T, tenant *store.Tenant, runID string) {
	t.Helper()
	if err := tenant.CreateRun(context.Background(), store.Run{
		ID: runID, Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
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
	if err := appendBytes(st, "team:acme", "r1", 60); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if got := standing(t, st, "acme"); got.Tier != store.TeamTierFree || got.EventBytes != 60 {
		t.Fatalf("after the first write = %+v, want a slot holding 60 bytes", got)
	}
	err := appendBytes(st, "team:acme", "r1", 41)
	var quota *store.StorageQuotaError
	if !errors.As(err, &quota) || quota.Limit != store.StorageLimitFreeEvents || !errors.Is(err, store.ErrStorageQuota) {
		t.Fatalf("a write past the share = %v, want a free event share refusal", err)
	}
	if !strings.Contains(err.Error(), "60 of 100 bytes") || !strings.Contains(err.Error(), "add credits") {
		t.Fatalf("refusal = %q, want it to name the share and the remedy", err)
	}
	if err := appendBytes(st, "team:acme", "r1", 40); err != nil {
		t.Fatalf("a write that fills the share exactly: %v", err)
	}
	fund(t, acme)
	if err := appendBytes(st, "team:acme", "r1", 500); err != nil {
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
	if err := appendBytes(st, "admin", "r1", 500); err != nil {
		t.Fatalf("an operator write past the share: %v", err)
	}
	if taken, _, err := st.FreeSlots(context.Background()); err != nil || taken != 0 {
		t.Fatalf("slots taken = %d, %v; want none", taken, err)
	}
}

func TestExpiryAndReconcileLowerAFreeTeamsEventBytes(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	acme := teamHandle(t, st, "acme")
	now := time.Now()
	for _, id := range []string{"old", "gone", "new"} {
		teamRun(t, acme, id)
		if err := appendBytes(st, "team:acme", id, 100); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().Exec(storetest.Rebind(st,
		`UPDATE runs SET status = 'success', finished_at = ? WHERE id = 'old'`), now.Add(-31*24*time.Hour).UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStorageSettings(ctx, store.StorageSettings{EventRetentionDays: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExpireRetainedRuns(ctx, now); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, "acme"); got.EventBytes != 200 {
		t.Fatalf("after expiry = %d, want the two live runs' 200", got.EventBytes)
	}
	if err := st.DeleteRun(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReconcileFreeEventBytes(ctx); err != nil {
		t.Fatal(err)
	}
	if got := standing(t, st, "acme"); got.EventBytes != 100 {
		t.Fatalf("after the reconcile = %d, want the one run left", got.EventBytes)
	}
}
