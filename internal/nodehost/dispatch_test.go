package nodehost

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func releaseRemote(t *testing.T, f *storeHostFixture, node string) {
	t.Helper()
	f.Host.Local = func(store.NodeRow) bool { return false }
	ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: runspec.NodeID(node)}
	row, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Host.release(t.Context(), f.Input, f.Accepted, entry, row, store.DecisionReady, func(context.Context, store.ReconcileRequest) error {
		t.Fatal("remote work entered owner executor")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func claimRemote(t *testing.T, f *storeHostFixture, node string) (store.UnitClaim, NodeInput) {
	t.Helper()
	claim, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: runspec.RunID(f.Input.RunID), NodeID: runspec.NodeID(node)}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: runspec.NodeID(node)})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	input, err := NewNodeInput(f.Accepted, row, entry.Engine, f.Input.GrantedRunner)
	if err != nil {
		t.Fatal(err)
	}
	return claim, input
}

func remotePlan() nodeproto.PlanDoc {
	return nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "test", Needs: []string{"build"}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}}
}

func TestDispatchAdoptsTheRowTheRemoteHostWrote(t *testing.T) {
	f := newStoreHostFixture(t, remotePlan())
	for _, id := range []string{"build", "test"} {
		releaseRemote(t, f, id)
		_, input := claimRemote(t, f, id)
		if err := f.report(t.Context(), input, store.NodeSuccess); err != nil {
			t.Fatal(err)
		}
		before, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: runspec.NodeID(id)})
		if err != nil {
			t.Fatal(err)
		}
		result, err := FollowReconciliation(t.Context(), f.State, store.ReconcileRequest{Ref: before.Ref, Actor: f.Actor, Policy: store.SourceNone}, nil)
		if err != nil || result.Kind != store.KeepTerminal || result.Node.Outcome != store.NodeSuccess || result.Node.StateSeq != before.StateSeq || result.Node.Attempt != 1 || result.Node.Generation != input.Generation {
			t.Fatalf("adopted = %+v, error %v", result, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	completion, err := f.Host.Run(ctx, f.Input)
	if err != nil || completion.Outcome != "success" {
		t.Fatalf("completion = %+v, error %v", completion, err)
	}
}

func TestDispatchSendsANodeOnceWhateverItsRetries(t *testing.T) {
	f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "flaky", Retry: &nodeproto.RetrySpec{Attempts: 3}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}})
	releaseRemote(t, f, "flaky")
	_, input := claimRemote(t, f, "flaky")
	ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "flaky"}
	held, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		result, err := f.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: store.SourceNone})
		if err != nil || result.Kind != store.WaitHolder || result.Node.Generation != held.Generation || result.Node.StateSeq != held.StateSeq {
			t.Fatalf("held reconciliation = %+v, error %v", result, err)
		}
	}
	if _, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}}); err == nil {
		t.Fatal("held node awarded twice")
	}
	if err := f.report(t.Context(), input, store.NodeFailed); err != nil {
		t.Fatal(err)
	}
	completion, err := f.Host.Run(t.Context(), f.Input)
	if err != nil || completion.Outcome != "failed" {
		t.Fatalf("completion = %+v, error %v", completion, err)
	}
}

func TestDispatchCancelledRunCancelsTheOutstandingNode(t *testing.T) {
	f := newStoreHostFixture(t, remotePlan())
	releaseRemote(t, f, "build")
	_, input := claimRemote(t, f, "build")
	ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Input.RunID), NodeID: "build"}
	if _, err := f.Tenant.TransitionRun(t.Context(), store.RunTransition{RunID: ref.RunID, Actor: f.Control, To: store.RunCancelled, Cause: store.RunCancel}); err != nil {
		t.Fatal(err)
	}
	f.State.notify()
	applied, err := f.State.TransitionNode(t.Context(), store.CancelNode{Ref: ref, Actor: f.Control})
	if err != nil || !applied.CancelRequested {
		t.Fatalf("cancel = %+v, error %v", applied, err)
	}
	held, err := f.Tenant.ReadNode(t.Context(), ref)
	if err != nil || held.ClaimedBy != input.Authority.Holder || held.Generation != input.Generation || held.Outcome != "" || held.CancelRequestedAt == nil {
		t.Fatalf("cancelled holder = %+v, error %v", held, err)
	}
	if err := f.report(t.Context(), input, store.NodeCancelled); err != nil {
		t.Fatal(err)
	}
	entry, err := f.Host.Acquire(t.Context(), f.Input)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := f.Host.schedule(t.Context(), f.Input, f.Accepted, entry)
	if err != nil || completion.Outcome != "cancelled" {
		t.Fatalf("completion = %+v, error %v", completion, err)
	}
	dependent, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: ref.RunID, NodeID: "test"})
	if err != nil || dependent.Outcome != store.NodeCancelled || dependent.Attempt != 0 {
		t.Fatalf("dependent = %+v, error %v", dependent, err)
	}
}

func TestDispatchLeavesConcurrencyToRemoteExecution(t *testing.T) {
	for _, compute := range []bool{false, true} {
		for _, scope := range []string{"run", "box"} {
			for _, mode := range []string{"fresh", "ready", "claimed"} {
				t.Run(fmt.Sprintf("local_admission=%t/%s/%s", compute, scope, mode), func(t *testing.T) {
					plan := remotePlan()
					for i := range plan.Jobs {
						plan.Jobs[i].Concurrency = []nodeproto.ConcurrencySpec{{Group: "db", Scope: new(scope)}}
						plan.Jobs[i].Resources = &nodeproto.ResourceSpec{Cores: new(8.0), MemoryBytes: new(int64(8 << 30))}
					}
					f := newStoreHostFixture(t, plan)
					claims := map[string]NodeInput{}
					if mode != "fresh" {
						releaseRemote(t, f, "build")
					}
					if mode == "claimed" {
						_, claims["build"] = claimRemote(t, f, "build")
					}
					f.Host.Local = func(store.NodeRow) bool { return false }
					d := newFakeDaemon()
					d.hostGate = make(chan struct{})
					var machine *LocalAdmission
					if compute {
						machine = &LocalAdmission{Daemon: d}
					}
					f.Host.executionAdmission = ComposeStartGate(f.Tenant, machine, false)
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					done := make(chan error, 1)
					joined := make(chan struct{})
					go func() {
						defer close(joined)
						completion, err := f.Host.Run(ctx, f.Input)
						if err == nil && completion.Outcome != "success" {
							err = fmt.Errorf("completion=%+v", completion)
						}
						done <- err
					}()
					defer func() { cancel(); <-joined }()
					for _, id := range []string{"build", "test"} {
						for {
							snapshot, err := f.Tenant.ReadSnapshot(ctx, f.Input.RunID)
							if err != nil {
								t.Fatal(err)
							}
							row, ok := ownerNode(snapshot, id)
							if ok && row.ReadyAt != nil {
								break
							}
							if err := f.Host.Wait(ctx, snapshot); err != nil {
								t.Fatal(err)
							}
						}
						input, claimed := claims[id]
						if !claimed {
							_, input = claimRemote(t, f, id)
						}
						admission := ComposeStartGate(f.Tenant, nil, false)
						if err := admission.claimedNode(ctx, input, func(ctx context.Context) error {
							key := fixtureJobCapacityKey("db", scope, f.Input.RunID, f.Actor.Owner.Holder)
							state, err := f.Tenant.GetConcurrencyState(ctx, key)
							if err != nil || len(state.Holders) != 1 || state.Holders[0].NodeID != id {
								return fmt.Errorf("claimed job lost concurrency: %+v %v", state, err)
							}
							return f.report(ctx, input, store.NodeSuccess)
						}); err != nil {
							t.Fatal(err)
						}

					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if live, granted, released := d.counts(); live != 0 || granted != 0 || released != 0 {
						t.Fatalf("leases=%d/%d/%d", live, granted, released)
					}
					state, err := f.Tenant.GetConcurrencyState(ctx, fixtureJobCapacityKey("db", scope, f.Input.RunID, f.Actor.Owner.Holder))
					if err != nil || len(state.Holders) != 0 || len(state.Waiters) != 0 {
						t.Fatalf("remote concurrency leaked: %+v, %v", state, err)
					}
					for _, req := range d.requests() {
						if len(req.Semaphores) != 0 || req.Resources != (wingwire.HostResources{}) {
							t.Fatalf("remote work reserved machine resources: %+v", req)
						}
					}
				})
			}
		}
	}
}

func TestDispatchStoreCancellationDrainsTheRemoteHolder(t *testing.T) {
	plan := remotePlan()
	plan.Jobs = plan.Jobs[:1]
	plan.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "db", Scope: new("global"), OnLimit: new("cancel_others")}}
	f := newStoreHostFixture(t, plan)
	f.Host.Local = func(store.NodeRow) bool { return false }
	d := newFakeDaemon()
	f.Host.executionAdmission = ComposeStartGate(f.Tenant, &LocalAdmission{Daemon: d}, false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, err := f.Host.Run(ctx, f.Input)
		done <- err
	}()
	defer func() { cancel(); <-joined }()
	for {
		snapshot, err := f.Tenant.ReadSnapshot(ctx, f.Input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if row, _ := ownerNode(snapshot, "build"); row.ReadyAt != nil {
			break
		}
		if err := f.Host.Wait(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	_, input := claimRemote(t, f, "build")
	admission := ComposeStartGate(f.Tenant, nil, false)
	if err := admission.claimedNode(ctx, input, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}

	replacement := newStoreHostFixtureOn(t, f.Store, plan, runspec.Options{})
	releaseRemote(t, replacement, "build")
	_, replacementInput := claimRemote(t, replacement, "build")
	if err := ComposeStartGate(replacement.Tenant, nil, false).claimedNode(ctx, replacementInput, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.State.notify()
	for {
		snapshot, err := f.Tenant.ReadSnapshot(ctx, f.Input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		row, _ := ownerNode(snapshot, "build")
		if row.CancelRequestedAt != nil {
			if row.Status == store.StatusDone || row.ClaimedBy != input.Authority.Holder || row.Generation != input.Generation {
				t.Fatal("cancellation replaced the holder's result authority")
			}
			break
		}
		if err := f.Host.Wait(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	key := fixtureJobCapacityKey("db", "global", f.Input.RunID, "")
	state, err := f.Tenant.GetConcurrencyState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	held := false
	for _, holder := range state.Holders {
		if holder.RunID == f.Input.RunID && holder.NodeID == "build" && holder.Superseded {
			held = true
		}
	}
	if !held {
		t.Fatalf("cancellation released the remote logical holder before it drained: %+v", state)
	}
	run, err := f.Tenant.ReadRun(ctx, f.Input.RunID)
	if err != nil || run.FinishedAt != nil {
		t.Fatalf("owner finished before the remote holder drained: %+v %v", run, err)
	}
	select {
	case err := <-done:
		t.Fatalf("owner returned before the remote holder drained: %v", err)
	default:
	}
	if err := f.report(ctx, input, store.NodeCancelled); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state, err = f.Tenant.GetConcurrencyState(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, holder := range state.Holders {
		if holder.RunID == f.Input.RunID {
			t.Fatalf("remote logical holder survived draining: %+v", state)
		}
	}
	if live, granted, released := d.counts(); live != 0 || granted != 0 || released != 0 {
		t.Fatalf("leases=%d/%d/%d", live, granted, released)
	}
}
