package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestCapacitySupersessionCancelsCurrentAwardAfterReclaim(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			ctx := t.Context()
			f := newStoreRecoveryFixtureAt(t, false, dialect.target(t), func(plan *nodeproto.PlanDoc) {
				plan.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "deploy", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new("cancel_others")}}
			})
			binding := runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
			if result, err := f.tenant.MayStart(ctx, binding, f.holder); err != nil || result.Kind != runstate.AcquireGranted {
				t.Fatalf("first award admission: %+v, %v", result, err)
			}
			type reservation struct {
				id                  string
				arrival, generation int64
			}
			readReservation := func() reservation {
				t.Helper()
				var r reservation
				if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT holder_id,queue_arrived_at,generation FROM concurrency_holders WHERE team=? AND run_id=? AND node_id=?`), f.tenant.Team(), f.ref.RunID, f.ref.NodeID).Scan(&r.id, &r.arrival, &r.generation); err != nil {
					t.Fatal(err)
				}
				return r
			}
			before := readReservation()
			if err := f.expireHolder(ctx); err != nil {
				t.Fatal(err)
			}
			reaped, err := f.s.AsOperator().ReapUnits(ctx, f.lease, 10)
			if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
				t.Fatalf("unstarted award recovery: %+v, %v", reaped, err)
			}
			next, err := f.tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: f.ref, Actor: f.owner, Policy: runstate.SourceResume})
			if err != nil || next.Kind != runstate.Execute || next.Claim == nil || int64(next.Claim.Generation) != binding.Generation+1 {
				t.Fatalf("replacement award: %+v, %v", next, err)
			}
			claim := next.Claim
			f.holder = runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.CapabilityPrefix}, CapabilityPrefix: claim.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: f.ref.RunID, NodeID: f.ref.NodeID, Holder: claim.Holder, Generation: claim.Generation}}
			binding.Generation = int64(claim.Generation)
			if result, err := f.tenant.MayStart(ctx, binding, f.holder); err != nil || result.Kind != runstate.AcquireGranted {
				t.Fatalf("replacement award admission: %+v, %v", result, err)
			}
			if retained := readReservation(); retained != before || retained.generation == binding.Generation {
				t.Fatalf("claim replacement changed work reservation: before=%+v after=%+v award=%d", before, retained, binding.Generation)
			}
			if _, err := f.tenant.TransitionNode(ctx, store.StartNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			competitorBinding, competitor := admissionPolicyBlocker(t, f)
			if result, err := f.tenant.MayStart(ctx, competitorBinding, competitor); err != nil || result.Kind != runstate.AcquireGranted {
				t.Fatalf("superseding admission: %+v, %v", result, err)
			}
			row, err := f.tenant.ReadNode(ctx, f.ref)
			if err != nil || row.Generation != claim.Generation || row.Status != runstate.StatusRunning || row.CancelRequestedAt == nil || row.CancelReason != "concurrency superseded" {
				t.Fatalf("supersession: generation=%d status=%s cancelled_at=%v reason=%q: %v", row.Generation, row.Status, row.CancelRequestedAt, row.CancelReason, err)
			}
			var granted bool
			if err := f.s.DB().QueryRowContext(ctx, storetest.Rebind(f.s, `SELECT granted FROM admission_work WHERE team=? AND run_id=? AND node_id=?`), f.tenant.Team(), f.ref.RunID, f.ref.NodeID).Scan(&granted); err != nil || granted {
				t.Fatalf("superseded work retained execution permit: granted=%t, %v", granted, err)
			}
		})
	}
}
