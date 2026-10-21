package store_test

import (
	"crypto/sha256"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestExecutionStartRequiresAwardBoundAdmission(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			f := newStoreRecoveryFixtureAt(t, false, dialect.target(t))
			assertProcessPermitSnapshot(t, f, false)
			start := store.StartNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)}
			if _, err := f.tenant.TransitionNode(t.Context(), start); err == nil {
				t.Fatal("job without groups bypassed MayStart")
			}
			assertNoJobPermit(t, f)
			row, err := f.tenant.ReadNode(t.Context(), f.ref)
			if err != nil || row.StartedAt != nil || row.Attempt != 0 {
				t.Fatalf("refused start changed attempt: %+v %v", row, err)
			}
			binding := store.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
			bad := f.holder
			bad.NodeFence.Generation++
			binding.Generation++
			if _, err := f.tenant.MayStart(t.Context(), binding, bad); err == nil {
				t.Fatal("stale award obtained admission")
			}
			assertNoJobPermit(t, f)
			binding.Generation = int64(f.holder.Generation)
			if result, err := f.tenant.MayStart(t.Context(), binding, f.holder); err != nil || result.Kind != store.AcquireGranted {
				t.Fatalf("admission: %+v %v", result, err)
			}
			assertProcessPermitSnapshot(t, f, false)
			stale := start
			stale.Actor = bad
			if _, err := f.tenant.TransitionNode(t.Context(), stale); err == nil {
				t.Fatal("permit authorized another award")
			}
			if _, err := f.tenant.TransitionNode(t.Context(), start); err != nil {
				t.Fatal(err)
			}
			assertProcessPermitSnapshot(t, f, true)
			if _, err := f.s.DB().ExecContext(t.Context(), storetest.Rebind(f.s, `UPDATE admission_work SET granted=0 WHERE team=? AND run_id=? AND node_id=?`), f.tenant.Team(), f.ref.RunID, f.ref.NodeID); err != nil {
				t.Fatal(err)
			}
			assertProcessPermitSnapshot(t, f, false)
		})
	}
}

func assertNoJobPermit(t *testing.T, f *storeRecoveryFixture) {
	t.Helper()
	var count int
	if err := f.s.DB().QueryRowContext(t.Context(), storetest.Rebind(f.s, `SELECT COUNT(*) FROM admission_work WHERE team=? AND run_id=? AND node_id=?`), f.tenant.Team(), f.ref.RunID, f.ref.NodeID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused admission persisted %d permits: %v", count, err)
	}
}

func TestJobAdmissionPolicyCommitsTerminalOutcome(t *testing.T) {
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			for _, policy := range []string{"skip", "fail"} {
				t.Run(policy, func(t *testing.T) {
					f := newStoreRecoveryFixtureAt(t, false, dialect.target(t), func(doc *nodeproto.PlanDoc) {
						doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "deploy", Scope: new("global"), Capacity: new(1), Cost: new(1), OnLimit: new(policy)}}
					})
					blockerBinding, blocker := admissionPolicyBlocker(t, f)
					if result, err := f.tenant.MayStart(t.Context(), blockerBinding, blocker); err != nil || result.Kind != store.AcquireGranted {
						t.Fatalf("blocker: %+v %v", result, err)
					}
					binding := store.CapacityBinding{RunID: string(f.ref.RunID), NodeID: string(f.ref.NodeID), Generation: int64(f.holder.Generation)}
					result, err := f.tenant.MayStart(t.Context(), binding, f.holder)
					wantKind, wantOutcome := store.AcquireSkipped, store.NodeSkipped
					if policy == "fail" {
						wantKind, wantOutcome = store.AcquireFailed, store.NodeFailed
					}
					if err != nil || result.Kind != wantKind {
						t.Fatalf("policy: %+v %v, want %s", result, err, wantKind)
					}
					row, err := f.tenant.ReadNode(t.Context(), f.ref)
					if err != nil || row.Status != store.StatusDone || row.Outcome != wantOutcome || row.Attempt != 0 {
						t.Fatalf("policy did not settle unstarted award: %+v %v", row, err)
					}
					assertNoJobPermit(t, f)
					events, err := f.tenant.ReadStateEvents(t.Context(), string(f.ref.RunID), 0, 100)
					if err != nil {
						t.Fatal(err)
					}
					terminal := 0
					for _, event := range events {
						if event.NodeID == string(f.ref.NodeID) && event.Kind == "job_"+string(wantOutcome) {
							terminal++
						}
					}
					if terminal != 1 {
						t.Fatalf("terminal events=%d: %+v", terminal, events)
					}
					if _, err := f.tenant.TransitionNode(t.Context(), store.StartNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)}); err == nil {
						t.Fatal("terminal policy outcome still allowed body start")
					}
				})
			}
		})
	}
}

func admissionPolicyBlocker(t *testing.T, f *storeRecoveryFixture) (store.CapacityBinding, store.Actor) {
	t.Helper()
	ctx := t.Context()
	intake, err := f.tenant.AdmitRun(ctx, store.IntakeRequest{Authority: f.plan.Seal.Request.AuthorityRef, Request: f.plan.Seal.Request.Request})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := f.claimOwner(ctx, intake.RunID, "admission-blocker")
	if err != nil {
		t.Fatal(err)
	}
	plan := f.plan
	plan.RunID, plan.Actor, plan.Seal.Request = intake.RunID, owner, intake.Request
	if _, err := f.tenant.AcceptPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	ref := controllerwire.NodeRef{RunID: runspec.RunID(intake.RunID), NodeID: f.ref.NodeID}
	if _, err := f.tenant.TransitionNode(ctx, store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	release, err := f.tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: owner, Policy: store.SourceResume})
	if err != nil || release.Kind != store.Execute || release.Claim == nil {
		t.Fatalf("blocker award: %+v %v", release, err)
	}
	claim := release.Claim
	actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.CapabilityPrefix}, CapabilityPrefix: claim.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: claim.Holder, Generation: claim.Generation}}
	return store.CapacityBinding{RunID: intake.RunID, NodeID: string(ref.NodeID), Generation: int64(claim.Generation)}, actor
}

func assertProcessPermitSnapshot(t *testing.T, f *storeRecoveryFixture, want bool) {
	t.Helper()
	snapshot, err := f.s.AsOperator().ReadAuthentication(t.Context(), capability.CredentialLookup{Class: capability.CredentialBearer, Audience: capability.Controller, Digest: sha256.Sum256([]byte(f.holderBearer))}, capability.RequestEvidence{})
	if err != nil || snapshot.Binding.StartPermitted != want {
		t.Fatalf("authentication start permit=%t want %t: %v", snapshot.Binding.StartPermitted, want, err)
	}
}
