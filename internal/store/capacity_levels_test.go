package store_test

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestCapacityLevelsBreakOpposingPlanJobCycle(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *storetest.Target{"sqlite": storetest.NewSQLite, "postgres": storetest.NewPostgres} {
		t.Run(name, func(t *testing.T) {
			target := open(t)
			group := func(name string) nodeproto.ConcurrencySpec {
				return nodeproto.ConcurrencySpec{Group: name, Capacity: new(1), Cost: new(1), Scope: new("global"), OnLimit: new("queue")}
			}
			f := newStoreRecoveryFixtureAt(t, false, target, func(doc *nodeproto.PlanDoc) {
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
			if result, err := f.tenant.MayStart(ctx, runstate.CapacityBinding{RunID: string(f.ref.RunID), NodeID: "job", Generation: int64(f.holder.Generation)}, f.holder); err != nil || result.Kind != runstate.AcquireGranted {
				t.Fatalf("first job admission: %+v, %v", result, err)
			}
			if _, err := f.tenant.TransitionNode(ctx, store.StartNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(0)}); err != nil {
				t.Fatal(err)
			}

			ref := runspec.NodeRef{RunID: runspec.RunID(admitted.RunID), NodeID: "job"}
			if _, err := f.tenant.TransitionNode(ctx, store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
				t.Fatal(err)
			}
			next, err := f.tenant.ReconcileNode(ctx, store.ReconcileRequest{Ref: ref, Actor: owner, Policy: runstate.SourceResume})
			if err != nil || next.Kind != runstate.Execute || next.Claim == nil {
				t.Fatalf("second run did not execute: %+v, %v", next, err)
			}
			holder := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: next.Claim.CapabilityPrefix}, CapabilityPrefix: next.Claim.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: next.Claim.Holder, Generation: next.Claim.Generation}}
			if result, err := f.tenant.MayStart(ctx, runstate.CapacityBinding{RunID: admitted.RunID, NodeID: "job", Generation: int64(holder.Generation)}, holder); err != nil || result.Kind != runstate.AcquireGranted {
				t.Fatalf("claimed job lost its reservation: %+v, %v", result, err)
			}
			if _, err := f.tenant.TransitionNode(ctx, store.StartNode{Ref: ref, Actor: holder, SourceAttempt: acceptanceAttempt(0)}); err != nil {
				t.Fatal(err)
			}
			for key, run := range map[string]string{"plan:global:X": string(f.ref.RunID), "plan:global:Y": admitted.RunID, "job:global:X": admitted.RunID, "job:global:Y": string(f.ref.RunID)} {
				state, err := f.tenant.GetConcurrencyState(ctx, key)
				if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != run || len(state.Waiters) != 0 {
					t.Fatalf("independent group %s: %+v, %v", key, state, err)
				}
			}
			if _, err := f.tenant.TransitionNode(ctx, store.ReportNode{Ref: f.ref, Actor: f.holder, Outcome: runstate.NodeSuccess, SourceAttempt: acceptanceAttempt(1)}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.tenant.TransitionNode(ctx, store.ReportNode{Ref: ref, Actor: holder, Outcome: runstate.NodeSuccess, SourceAttempt: acceptanceAttempt(1)}); err != nil {
				t.Fatal(err)
			}
			for _, run := range []struct {
				id    string
				actor runstate.Actor
			}{{string(f.ref.RunID), f.owner}, {admitted.RunID, owner}} {
				if _, err := f.tenant.FinishRunAndUnit(ctx, runstate.FinishRunRequest{Transition: runstate.RunCompletion{RunID: runspec.RunID(run.id), Actor: run.actor, Cause: runstate.RunFinish}, Unit: run.actor.Owner}); err != nil {
					t.Fatalf("finish %s: %v", run.id, err)
				}
			}
		})
	}
}
