package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestClaimedAdmissionWaitsForPlanBeforeTakingJobCapacity(t *testing.T) {
	f := newRecoveryCapacityFixture(t)
	ctx := t.Context()
	competitor, err := f.capacityCompetitor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	occupied, err := f.tenant.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{Key: "plan:global:plan", HolderID: "competitor", RunID: competitor, Capacity: 1, Cost: 1, Lease: time.Minute, Policy: store.OnLimitQueue})
	if err != nil || occupied.Kind != store.AcquireGranted {
		t.Fatalf("occupy plan capacity: %+v, %v", occupied, err)
	}
	binding := store.CapacityBinding{RunID: f.ref.RunID, NodeID: f.ref.NodeID, Generation: f.holder.Generation}
	result, err := f.tenant.AdmitCapacity(ctx, binding, f.holder)
	if err != nil || result.Kind != store.AcquireQueued {
		t.Fatalf("claimed admission bypassed plan capacity: %+v, %v", result, err)
	}
	for _, key := range []string{"job:global:node", "job:global:deploy"} {
		state, err := f.tenant.GetConcurrencyState(ctx, key)
		if err != nil && !errors.Is(err, store.ErrNotFound) || state != nil && (len(state.Holders) != 0 || len(state.Waiters) != 0) {
			t.Fatalf("job capacity taken before plan capacity for %s: %+v, %v", key, state, err)
		}
	}
	if _, _, _, err := f.tenant.ReleaseAndNotify(ctx, "plan:global:plan", "competitor", "success", "", "", 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	result, err = f.tenant.AdmitCapacity(ctx, binding, f.holder)
	if err != nil || result.Kind != store.AcquireGranted {
		t.Fatalf("claimed admission after plan promotion: %+v, %v", result, err)
	}
	for _, key := range []string{"plan:global:plan", "job:global:node", "job:global:deploy"} {
		state, err := f.tenant.GetConcurrencyState(ctx, key)
		if err != nil || state == nil || len(state.Holders) != 1 || state.Holders[0].RunID != f.ref.RunID {
			t.Fatalf("capacity was not admitted for %s: %+v, %v", key, state, err)
		}
	}
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s, `UPDATE concurrency_holders SET lease_expires_at=1 WHERE team=? AND key='plan:global:plan' AND run_id=?`), f.tenant.Team(), f.ref.RunID); err != nil {
		t.Fatal(err)
	}
	occupied, err = f.tenant.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{Key: "plan:global:plan", HolderID: "competitor", RunID: competitor, Capacity: 1, Cost: 1, Lease: time.Minute, Policy: store.OnLimitQueue})
	if err != nil || occupied.Kind != store.AcquireGranted {
		t.Fatalf("occupy expired plan capacity: %+v, %v", occupied, err)
	}
	result, err = f.tenant.AdmitCapacity(ctx, binding, f.holder)
	if err != nil || result.Kind != store.AcquireQueued {
		t.Fatalf("recovery bypassed lost plan capacity: %+v, %v", result, err)
	}
	for _, key := range []string{"job:global:node", "job:global:deploy"} {
		state, err := f.tenant.GetConcurrencyState(ctx, key)
		if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
			t.Fatalf("recovery retained later capacity while waiting for plan: %s: %+v, %v", key, state, err)
		}
	}
}

func TestClaimedAdmissionRetainsPlanThroughOwnerRecovery(t *testing.T) {
	f := newRecoveryCapacityFixture(t)
	ctx := t.Context()
	binding := store.CapacityBinding{RunID: f.ref.RunID, NodeID: f.ref.NodeID, Generation: f.holder.Generation}
	result, err := f.tenant.AdmitCapacity(ctx, binding, f.holder)
	if err != nil || result.Kind != store.AcquireGranted {
		t.Fatalf("initial admission: %+v, %v", result, err)
	}
	before, err := f.tenant.GetConcurrencyState(ctx, "plan:global:plan")
	if err != nil || len(before.Holders) != 1 {
		t.Fatalf("initial plan reservation: %+v, %v", before, err)
	}
	now, err := f.s.TestOnlyDatabaseTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB().ExecContext(ctx, storetest.Rebind(f.s, `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=?`), now.UnixNano(), f.tenant.Team(), f.ref.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.RenewNodeClaim(ctx, f.ref, f.holder); err != nil {
		t.Fatal(err)
	}
	if units, err := f.s.AsOperator().ReapUnits(ctx, f.lease, 10); err != nil || len(units) != 1 || !units[0].Requeued {
		t.Fatalf("owner recovery: %+v, %v", units, err)
	}
	owner, err := f.claimOwner(ctx, f.ref.RunID, "adopting-owner")
	if err != nil {
		t.Fatal(err)
	}
	result, err = f.tenant.AdmitCapacity(ctx, store.CapacityBinding{RunID: f.ref.RunID, Generation: owner.Owner.Generation}, owner)
	if err != nil || result.Kind != store.AcquireGranted {
		t.Fatalf("successor admission: %+v, %v", result, err)
	}
	after, err := f.tenant.GetConcurrencyState(ctx, "plan:global:plan")
	if err != nil || len(after.Holders) != 1 || !after.Holders[0].ClaimedAt.Equal(before.Holders[0].ClaimedAt) || after.Holders[0].HolderID != before.Holders[0].HolderID {
		t.Fatalf("recovery replaced continuous reservation: %+v -> %+v, %v", before, after, err)
	}
}
