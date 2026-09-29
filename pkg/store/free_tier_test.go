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

func storeBytes(st *store.Store, team store.Team) error {
	res, err := reserve(st, team, store.StorageCache, 1, time.Now())
	if err != nil {
		return err
	}
	return st.CommitStorage(context.Background(), store.StorageCommit{
		ID: res.ID, Team: team, Kind: store.StorageCache, Bytes: 1, Now: time.Now(),
	})
}

// The free tier is full the instant its last slot is taken: no sample, no
// pass, no ceiling on bytes. A team takes a slot with its first stored byte,
// a funded team needs none, and only deleting a team gives its slot back.
func TestFreeSlotsBoundTheFreeTierAtEveryInstant(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	if err := st.SetFreeTeamSlots(ctx, 2); err != nil {
		t.Fatal(err)
	}
	a, b, c := teamHandle(t, st, "team-a"), teamHandle(t, st, "team-b"), teamHandle(t, st, "team-c")
	for _, tenant := range []*store.Tenant{a, b, c} {
		if err := startRun(tenant, "t-"+string(tenant.Team())); err != nil {
			t.Fatalf("%s's run: %v", tenant.Team(), err)
		}
	}
	if got := standing(t, st, "team-a"); got.Tier != store.TeamTierNone {
		t.Fatalf("a team that ran but stored nothing = %+v, want no slot yet", got)
	}
	for _, team := range []store.Team{"team-a", "team-b"} {
		if err := storeBytes(st, team); err != nil {
			t.Fatalf("%s's first stored byte: %v", team, err)
		}
	}
	err := storeBytes(st, "team-c")
	if !errors.Is(err, store.ErrFreeStoragePaused) || !strings.Contains(err.Error(), "buy credits or join the waitlist") {
		t.Fatalf("a third team with two slots = %v, want free storage paused", err)
	}
	if got := standing(t, st, "team-a"); got.Tier != store.TeamTierFree || got.AllowanceBytes != store.DefaultFreeAllowanceBytes {
		t.Fatalf("team-a = %+v, want free with the default allowance", got)
	}
	if got := standing(t, st, "team-c"); got.Tier != store.TeamTierNone {
		t.Fatalf("team-c = %+v, want no slot", got)
	}

	d := teamHandle(t, st, "team-d")
	fund(t, d)
	if err := storeBytes(st, "team-d"); err != nil {
		t.Fatalf("a funded team's write: %v", err)
	}
	if taken, limit, err := st.FreeSlots(ctx); err != nil || taken != 2 || limit != 2 {
		t.Fatalf("slots = %d of %d, %v; want the funded team to take none", taken, limit, err)
	}

	deleteAndPurge(t, st, "team-b", now)
	if err := storeBytes(st, "team-c"); err != nil {
		t.Fatalf("a write after a slotted team was deleted: %v", err)
	}
	if err := st.GrantFreeSlot(ctx, "team-d", now); err != nil {
		t.Fatalf("the operator grants past the cap: %v", err)
	}
	if taken, _, err := st.FreeSlots(ctx); err != nil || taken != 3 {
		t.Fatalf("slots taken = %d, %v; want the operator's grant past the cap", taken, err)
	}
}

// safety: every team reserved while one slot was left, so each holds a
// reservation; their commits race for the slot, exactly one takes it, and
// every other commit is refused, so no team without a slot counts a byte.
func TestTheLastFreeSlotGoesToOneTeam(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.SetFreeTeamSlots(ctx, 1); err != nil {
		t.Fatal(err)
	}
	const racers = 6
	teams := make([]store.Team, racers)
	held := make([]store.StorageReservation, racers)
	for i := range teams {
		teams[i] = teamHandle(t, st, store.Team(fmt.Sprintf("racer-%d", i))).Team()
		res, err := reserve(st, teams[i], store.StorageCache, 1, time.Now())
		if err != nil {
			t.Fatalf("%s's reservation with a slot left: %v", teams[i], err)
		}
		held[i] = res
	}
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i, team := range teams {
		wg.Go(func() {
			errs[i] = st.CommitStorage(ctx, store.StorageCommit{
				ID: held[i].ID, Team: team, Kind: store.StorageCache, Bytes: 1, Now: time.Now(),
			})
		})
	}
	wg.Wait()
	committed := 0
	for i, err := range errs {
		switch {
		case err == nil:
			committed++
		case !errors.Is(err, store.ErrFreeStoragePaused):
			t.Fatalf("unexpected refusal: %v", err)
		default:
			if got := usageOf(t, st, teams[i], store.StorageCache); got.UsedBytes != 0 {
				t.Fatalf("%s was refused a slot yet counts %d bytes", teams[i], got.UsedBytes)
			}
		}
	}
	if taken, _, err := st.FreeSlots(ctx); err != nil || taken != 1 || committed != 1 {
		t.Fatalf("committed %d, slots taken %d, %v; want exactly one", committed, taken, err)
	}
}

// A slot goes with the first committed byte, so a reservation nobody commits
// never spends one of the tier's slots.
func TestAnAbandonedReservationTakesNoSlot(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.SetFreeTeamSlots(ctx, 1); err != nil {
		t.Fatal(err)
	}
	teamHandle(t, st, "team-a")
	teamHandle(t, st, "team-b")
	res, err := reserve(st, "team-a", store.StorageCache, 10, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseStorage(ctx, "team-a", res.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := reserve(st, "team-a", store.StorageLogs, 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if taken, _, err := st.FreeSlots(ctx); err != nil || taken != 0 {
		t.Fatalf("slots after reservations nobody committed = %d, %v; want none", taken, err)
	}
	if err := storeBytes(st, "team-b"); err != nil {
		t.Fatalf("a team committing its first byte: %v", err)
	}
	if got := standing(t, st, "team-b"); got.Tier != store.TeamTierFree {
		t.Fatalf("team-b after its first committed byte = %+v, want a slot", got)
	}
	if err := storeBytes(st, "team-a"); !errors.Is(err, store.ErrFreeStoragePaused) {
		t.Fatalf("team-a once the only slot is taken = %v, want free storage paused", err)
	}
}

// A run is never refused for billing: a team without credits or a slot, one
// past any daily count, and one held over a disputed payment all start runs,
// which their own machines execute.
func TestTriggersAreNeverRefusedForBilling(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.SetFreeTeamSlots(ctx, 0); err != nil {
		t.Fatal(err)
	}
	acme := teamHandle(t, st, "acme")
	for i := range 201 {
		if err := startRun(acme, fmt.Sprintf("t-%d", i)); err != nil {
			t.Fatalf("run %d of a team with no credits and no slot: %v", i, err)
		}
	}
	if taken, _, err := st.FreeSlots(ctx); err != nil || taken != 0 {
		t.Fatalf("slots taken = %d, %v; want a run to take none", taken, err)
	}
	held := teamHandle(t, st, "held")
	fund(t, held)
	if _, err := st.HoldTeamForDispute(ctx, "held", "dp_1", "pay-held", "dispute", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := startRun(held, "t-held"); err != nil {
		t.Fatalf("a run of a team held over a dispute: %v", err)
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

// The slot is enforced on every commit path: a team that reserved while a
// slot was open is refused its upload commit and its renewal once another
// team took the last slot, and counts nothing.
func TestEveryCommitPathRefusesATeamThatCannotTakeASlot(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	if err := st.SetFreeTeamSlots(ctx, 1); err != nil {
		t.Fatal(err)
	}
	teamHandle(t, st, "team-a")
	teamHandle(t, st, "team-b")
	now := time.Now()
	u, err := st.ReserveUpload(ctx, store.UploadRequest{
		Team: "team-a", RunID: "run-a", Kind: store.StorageCache, Key: "artifacts/blobs/" + strings.Repeat("a", 64),
		Size: 10, SHA256: strings.Repeat("a", 64), Principal: "runner-a", Provenance: "cloud", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := reserve(st, "team-a", store.StorageLogs, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeBytes(st, "team-b"); err != nil {
		t.Fatal(err)
	}
	if err := st.CommitUpload(ctx, "team-a", u.ID, u.Principal, now); !errors.Is(err, store.ErrFreeStoragePaused) {
		t.Fatalf("upload commit without a slot = %v, want free storage paused", err)
	}
	if _, err := st.CommittedObject(ctx, "team-a", u.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused upload was published: %v", err)
	}
	_, err = st.RenewStorage(ctx, store.StorageCommit{ID: res.ID, Team: "team-a", Kind: store.StorageLogs, Bytes: 10, Now: now},
		store.StorageReserve{Team: "team-a", Kind: store.StorageLogs, Bytes: 10, UpTo: true, Now: now})
	if !errors.Is(err, store.ErrFreeStoragePaused) {
		t.Fatalf("renewal without a slot = %v, want free storage paused", err)
	}
	for _, kind := range []store.StorageKind{store.StorageCache, store.StorageLogs} {
		if got := usageOf(t, st, "team-a", kind); got.UsedBytes != 0 {
			t.Fatalf("team-a's %s without a slot = %d, want 0", kind, got.UsedBytes)
		}
	}
}
