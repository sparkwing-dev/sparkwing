package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestUnclaimedFullGroupHonorsQueueTimeout(t *testing.T) {
	f := admissionFixture(t, storetest.New(t), 1, 1)
	admissionWant(t, f, admissionBinding(f), f.holder, runstate.AcquireGranted)
	plan, owner := admissionAcceptedPlan(t, f, func(doc *nodewire.PlanDoc) {
		timeout := int64(1000)
		doc.Jobs[0].Concurrency[0].QueueTimeoutMS = &timeout
	})
	ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: f.ref.NodeID}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	for _, elapsed := range []time.Duration{0, 999 * time.Millisecond, time.Second} {
		f.s.SetTestClock(t, func() time.Time { return now.Add(elapsed) })
		result, err := f.tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: owner, Policy: runstate.SourceResume})
		if err != nil {
			t.Fatal(err)
		}
		row, err := f.tenant.ReadNode(t.Context(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if elapsed < time.Second {
			if result.Kind != runstate.Dispatch || row.ClaimedBy != "" || row.Status != runstate.StatusPending {
				t.Fatalf("full-group job left the unclaimed wait: result=%s row=%+v", result.Kind, row)
			}
		} else if result.Kind != runstate.KeepTerminal || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeFailed || row.FailureReason != store.FailureQueueTimeout || row.Attempt != 0 || row.ClaimedBy != "" {
			t.Fatalf("unclaimed group wait ignored its 1s timeout: result=%s row=%+v", result.Kind, row)
		}
		if elapsed == 0 {
			team := f.tenant.Team()
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			f.s = f.target.Open(t, store.WithPolicies(sealStorePolicies))
			f.tenant, err = f.s.ForTeam(t.Context(), team)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestQueuedClaimExpiresAnUnclaimedFullGroup(t *testing.T) {
	f := admissionFixture(t, storetest.New(t), 1, 1)
	admissionWant(t, f, admissionBinding(f), f.holder, runstate.AcquireGranted)
	plan, owner := admissionAcceptedPlan(t, f, func(doc *nodewire.PlanDoc) {
		timeout := int64(1000)
		doc.Jobs[0].Concurrency[0].QueueTimeoutMS = &timeout
	})
	ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: f.ref.NodeID}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	demand := store.UnitDemand{CapabilityPrefix: owner.CapabilityPrefix, Kinds: []capability.UnitKind{capability.UnitNode}}
	for _, elapsed := range []time.Duration{0, time.Second} {
		f.s.SetTestClock(t, func() time.Time { return now.Add(elapsed) })
		if _, err := f.s.AsOperator().ClaimUnit(t.Context(), demand); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("full group claim: %v", err)
		}
	}
	row, err := f.tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Status != runstate.StatusDone || row.Outcome != runstate.NodeFailed || row.FailureReason != store.FailureQueueTimeout || row.ClaimedBy != "" || row.Attempt != 0 {
		t.Fatalf("queued poll did not expire unclaimed group wait: row=%+v err=%v", row, err)
	}
	events, err := f.tenant.ReadStateEvents(t.Context(), string(ref.RunID), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, e := range events {
		if e.NodeID == string(ref.NodeID) && e.Kind == "job_failed" {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events=%d: %+v", terminal, events)
	}
}

func TestPreAwardGroupWaitReachesMayStart(t *testing.T) {
	f := admissionFixture(t, storetest.New(t), 1, 1)
	admissionWant(t, f, admissionBinding(f), f.holder, runstate.AcquireGranted)
	plan, owner := admissionAcceptedPlan(t, f, func(doc *nodewire.PlanDoc) {
		timeout := int64(1000)
		doc.Jobs[0].Concurrency[0].QueueTimeoutMS = &timeout
	})
	ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: f.ref.NodeID}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f.s.SetTestClock(t, func() time.Time { return now })
	request := store.ReconcileRequest{Ref: ref, Actor: owner, Policy: runstate.SourceResume}
	if result, err := f.tenant.ReconcileNode(t.Context(), request); err != nil || result.Kind != runstate.Dispatch {
		t.Fatalf("initial wait: %+v %v", result, err)
	}
	current := now.Add(500 * time.Millisecond)
	f.s.SetTestClock(t, func() time.Time { return current })
	admissionFinishJob(t, f, admissionBinding(f), f.holder)
	result, err := f.tenant.ReconcileNode(t.Context(), request)
	if err != nil || result.Kind != runstate.Execute || result.Claim == nil {
		t.Fatalf("award: %+v %v", result, err)
	}
	c := result.Claim
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.CapabilityPrefix}, CapabilityPrefix: c.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: c.Holder, Generation: c.Generation}}
	current = now.Add(time.Second)
	response, err := f.tenant.MayStart(t.Context(), runstate.CapacityBinding{RunID: plan.RunID, NodeID: string(ref.NodeID), Generation: int64(c.Generation)}, actor)
	if err != nil || response.Kind != runstate.AcquireFailed || response.FailureReason != store.FailureQueueTimeout {
		t.Fatalf("award reset group wait: %+v %v", response, err)
	}
	row, err := f.tenant.ReadNode(t.Context(), ref)
	if err != nil || row.Status != runstate.StatusDone || row.FailureReason != store.FailureQueueTimeout || row.Attempt != 0 {
		t.Fatalf("timeout outcome: %+v %v", row, err)
	}
}

func TestPreAwardGroupGrantSurvivesItsOldWaitDeadline(t *testing.T) {
	f := admissionFixture(t, storetest.New(t), 1, 1)
	admissionWant(t, f, admissionBinding(f), f.holder, runstate.AcquireGranted)
	plan, owner := admissionAcceptedPlan(t, f, func(doc *nodewire.PlanDoc) {
		timeout := int64(1000)
		doc.Jobs[0].Concurrency[0].QueueTimeoutMS = &timeout
	})
	ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: f.ref.NodeID}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	current := now
	f.s.SetTestClock(t, func() time.Time { return current })
	request := store.ReconcileRequest{Ref: ref, Actor: owner, Policy: runstate.SourceResume}
	if result, err := f.tenant.ReconcileNode(t.Context(), request); err != nil || result.Kind != runstate.Dispatch {
		t.Fatalf("wait: %+v %v", result, err)
	}
	current = now.Add(500 * time.Millisecond)
	admissionFinishJob(t, f, admissionBinding(f), f.holder)
	result, err := f.tenant.ReconcileNode(t.Context(), request)
	if err != nil || result.Claim == nil {
		t.Fatalf("award: %+v %v", result, err)
	}
	c := result.Claim
	actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.CapabilityPrefix}, CapabilityPrefix: c.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: c.Holder, Generation: c.Generation}}
	binding := runstate.CapacityBinding{RunID: plan.RunID, NodeID: string(ref.NodeID), Generation: int64(c.Generation)}
	for _, elapsed := range []time.Duration{500 * time.Millisecond, 2 * time.Second} {
		current = now.Add(elapsed)
		if response, err := f.tenant.MayStart(t.Context(), binding, actor); err != nil || response.Kind != runstate.AcquireGranted {
			t.Fatalf("grant expired at %v: %+v %v", elapsed, response, err)
		}
	}
}

func TestMaterializeOnlyReconcileExpiresAGroupWait(t *testing.T) {
	f := admissionFixture(t, storetest.New(t), 1, 1)
	admissionWant(t, f, admissionBinding(f), f.holder, runstate.AcquireGranted)
	plan, owner := admissionAcceptedPlan(t, f, func(doc *nodewire.PlanDoc) {
		timeout := int64(1000)
		doc.Jobs[0].Concurrency[0].QueueTimeoutMS = &timeout
	})
	ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: f.ref.NodeID}
	if _, err := f.tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
		t.Fatal(err)
	}
	now, err := f.s.TestOnlyDatabaseTime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	current := now
	f.s.SetTestClock(t, func() time.Time { return current })
	request := store.ReconcileRequest{Ref: ref, Actor: owner, Policy: runstate.SourceResume}
	if result, err := f.tenant.ReconcileNode(t.Context(), request); err != nil || result.Kind != runstate.Dispatch {
		t.Fatalf("wait: %+v %v", result, err)
	}
	current = now.Add(time.Second)
	request.MaterializeOnly = true
	result, err := f.tenant.ReconcileNode(t.Context(), request)
	if err != nil || result.Kind != runstate.KeepTerminal || result.Node.Outcome != runstate.NodeFailed || result.Node.FailureReason != store.FailureQueueTimeout || result.Claim != nil {
		t.Fatalf("materialize-only bypassed timeout: %+v %v", result, err)
	}
}

func TestGroupQueueTimeoutSurvivesUnstartedReturnOrSkip(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "full at selection", true: "capacity taken after selection"}[race], func(t *testing.T) {
			f := admissionFixture(t, storetest.New(t), 1, 1)
			plan, owner := admissionAcceptedPlan(t, f, func(doc *nodewire.PlanDoc) {
				timeout := int64(1000)
				doc.Jobs[0].Concurrency[0].QueueTimeoutMS = &timeout
			})
			ref := runspec.NodeRef{RunID: runspec.RunID(plan.RunID), NodeID: f.ref.NodeID}
			if _, err := f.tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: owner}); err != nil {
				t.Fatal(err)
			}
			now, err := f.s.TestOnlyDatabaseTime(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			current := now
			f.s.SetTestClock(t, func() time.Time { return current })
			held := false
			hold := func() {
				if !held {
					admissionWant(t, f, admissionBinding(f), f.holder, runstate.AcquireGranted)
					held = true
				}
			}
			demand := store.UnitDemand{CapabilityPrefix: owner.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}}
			if !race {
				hold()
			}
			for _, elapsed := range []time.Duration{0, time.Second} {
				current = now.Add(elapsed)
				claim, err := f.s.AsOperator().ClaimUnit(t.Context(), demand)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					t.Fatal(err)
				}
				hold()
				if err == nil {
					c := claim.Award
					actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: c.CapabilityPrefix}, CapabilityPrefix: c.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: c.Holder, Generation: c.Generation}}
					binding := runstate.CapacityBinding{RunID: string(ref.RunID), NodeID: string(ref.NodeID), Generation: int64(c.Generation)}
					response, err := f.tenant.MayStart(t.Context(), binding, actor)
					if err != nil {
						t.Fatal(err)
					}
					if response.Kind == runstate.AcquireQueued {
						if err := f.tenant.RelinquishStart(t.Context(), binding, actor); err != nil {
							t.Fatal(err)
						}
					} else if response.Kind != runstate.AcquireFailed || response.FailureReason != store.FailureQueueTimeout {
						t.Fatalf("unexpected start decision: %+v", response)
					}
				}
			}
			row, err := f.tenant.ReadNode(t.Context(), ref)
			if err != nil || row.Status != runstate.StatusDone || row.FailureReason != store.FailureQueueTimeout || row.Attempt != 0 || row.ClaimedBy != "" {
				t.Fatalf("initial group timeout reset after return or skip: %+v %v", row, err)
			}
		})
	}
}
