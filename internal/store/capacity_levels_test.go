package store_test

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestCapacityLevelsBreakOpposingPlanJobCycle(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *storetest.Target{"sqlite": storetest.NewSQLite, "postgres": storetest.NewPostgres} {
		t.Run(name, func(t *testing.T) {
			target := open(t)
			group := func(name string) nodeproto.ConcurrencySpec {
				return nodeproto.ConcurrencySpec{Group: name, Capacity: new(1), Cost: new(1), Scope: new("global"), OnLimit: new("queue")}
			}
			f := newStoreRecoveryFixtureAt(t, true, target, func(doc *nodeproto.PlanDoc) {
				doc.Concurrency = []nodeproto.ConcurrencySpec{group("X")}
				doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{group("Y")}
			})
			ctx := t.Context()
			admitted, err := f.tenant.AdmitRun(ctx, store.IntakeRequest{Authority: f.plan.Seal.Request.AuthorityRef, Request: f.plan.Seal.Request.Request})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := f.claimOwner(ctx, admitted.RunID, "opposing-owner")
			if err != nil {
				t.Fatal(err)
			}
			request := f.plan
			request.RunID, request.Actor, request.Seal.Request = admitted.RunID, owner, admitted.Request
			var doc nodeproto.PlanDoc
			if err := json.Unmarshal(request.Seal.Plan, &doc); err != nil {
				t.Fatal(err)
			}
			doc.Concurrency, doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{group("Y")}, []nodeproto.ConcurrencySpec{group("X")}
			request.Seal.Plan, err = json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			request.Seal.Resolved.Settings, err = settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: request.Seal.Declarations, Plan: doc})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.tenant.AcceptPlan(ctx, request); err != nil {
				t.Fatal(err)
			}
			// Both runs retain their plan group before either asks for its job group.
			for _, run := range []struct {
				id    string
				actor store.Actor
			}{{f.ref.RunID, f.owner}, {admitted.RunID, owner}} {
				result, err := f.tenant.AdmitCapacity(ctx, store.CapacityBinding{RunID: run.id, Generation: run.actor.Owner.Generation}, run.actor)
				if err != nil || result.Kind != store.AcquireGranted {
					t.Fatalf("plan admission %s: %+v, %v", run.id, result, err)
				}
			}
			for _, run := range []struct {
				id    string
				actor store.Actor
			}{{f.ref.RunID, f.holder}, {admitted.RunID, owner}} {
				generation := run.actor.Generation
				if run.actor.Kind == store.ActorRunHost {
					generation = 0 // owner-authorized pending job
				}
				result, err := f.tenant.AdmitCapacity(ctx, store.CapacityBinding{RunID: run.id, NodeID: "job", Generation: generation}, run.actor)
				if err != nil || result.Kind != store.AcquireGranted {
					t.Fatalf("opposing plan/job cycle blocked %s: %+v, %v", run.id, result, err)
				}
			}
			for key, run := range map[string]string{"plan:global:X": f.ref.RunID, "plan:global:Y": admitted.RunID, "job:global:X": admitted.RunID, "job:global:Y": f.ref.RunID} {
				state, err := f.tenant.GetConcurrencyState(ctx, key)
				if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != run || len(state.Waiters) != 0 {
					t.Fatalf("independent group %s: %+v, %v", key, state, err)
				}
			}
			if _, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: controllerwire.NodeRef{RunID: f.ref.RunID, NodeID: "job"}, Actor: f.holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(1)}); err != nil {
				t.Fatal(err)
			}
			ref := controllerwire.NodeRef{RunID: admitted.RunID, NodeID: "job"}
			if _, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: owner, To: store.NodeReady, Cause: store.CauseRelease}); err != nil {
				t.Fatal(err)
			}
			next, err := f.tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: owner, Policy: store.SourceResume})
			if err != nil || next.Kind != store.Execute || next.Claim == nil {
				t.Fatalf("second run did not execute: %+v, %v", next, err)
			}
			holder := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: next.Claim.CapabilityPrefix}, CapabilityPrefix: next.Claim.CapabilityPrefix, Holder: next.Claim.Holder, Generation: next.Claim.Generation}
			if result, err := f.tenant.AdmitCapacity(ctx, store.CapacityBinding{RunID: admitted.RunID, NodeID: "job", Generation: holder.Generation}, holder); err != nil || result.Kind != store.AcquireGranted {
				t.Fatalf("claimed job lost its reservation: %+v, %v", result, err)
			}
			if _, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: holder, To: store.NodeRunning, Cause: store.CauseStart, SourceAttempt: acceptanceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.tenant.TransitionNode(ctx, store.NodeTransition{Ref: ref, Actor: holder, To: store.NodeSuccess, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(1)}); err != nil {
				t.Fatal(err)
			}
			for _, run := range []struct {
				id    string
				actor store.Actor
			}{{f.ref.RunID, f.owner}, {admitted.RunID, owner}} {
				if _, err := f.tenant.FinishRunAndUnit(ctx, store.FinishRunRequest{Transition: store.RunTransition{RunID: run.id, Actor: run.actor, To: store.RunSuccess, Cause: store.RunFinish}, Unit: run.actor.Owner}); err != nil {
					t.Fatalf("finish %s: %v", run.id, err)
				}
			}
		})
	}
}
