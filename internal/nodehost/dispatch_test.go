package nodehost

import (
	"context"
	"errors"
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
	ref := controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: node}
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
	claim, err := f.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: f.Input.RunID, NodeID: node}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	row, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: node})
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
		before, err := f.Tenant.ReadNode(t.Context(), controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: id})
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
	ref := controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: "flaky"}
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
	ref := controllerwire.NodeRef{RunID: f.Input.RunID, NodeID: "build"}
	if _, err := f.Tenant.TransitionRun(t.Context(), store.RunTransition{RunID: ref.RunID, Actor: f.Control, To: store.RunCancelled, Cause: store.RunCancel}); err != nil {
		t.Fatal(err)
	}
	f.State.notify()
	applied, err := f.State.TransitionNode(t.Context(), store.NodeTransition{Ref: ref, Actor: f.Control, To: store.NodeCancelled, Cause: store.CauseCancel})
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

func TestDispatchRetainsStoreConcurrencyUntilRemoteCompletion(t *testing.T) {
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
					machineLeases := 0
					if compute {
						machine = &LocalAdmission{Daemon: d, BoxID: testBox}
						machineLeases = 1
					}
					configureNativeAdmission(&f.Host, machine, &storeAdmission{Store: f.Tenant, Owner: f.Actor, BoxID: testBox}, false)
					admitted := make(chan string, 3)
					bind := f.Host.bindAdmission
					f.Host.bindAdmission = func(bound *OwnerHost, actor store.Actor) {
						bind(bound, actor)
						admit := bound.Admit
						bound.Admit = func(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
							return admit(ctx, accepted, row, func(ctx context.Context) error {
								admitted <- row.Ref.NodeID
								return body(ctx)
							})
						}
					}
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
						select {
						case node := <-admitted:
							if node != id {
								t.Fatalf("unexpected remote admission: %s", node)
							}
						case err := <-done:
							t.Fatalf("host returned before remote admission: %v", err)
						case <-ctx.Done():
							t.Fatal(context.Cause(ctx))
						}
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
						key := scopedConcurrencyKey("db", scope, f.Input.RunID, testBox)
						state, err := f.Tenant.GetConcurrencyState(ctx, key)
						if err != nil || len(state.Holders) != 1 || state.Holders[0].NodeID != id || state.UsedCost != 1 {
							t.Fatalf("ready remote job lost its concurrency: %+v, %v", state, err)
						}
						if live, _, _ := d.counts(); live != machineLeases {
							t.Fatalf("remote job reserved machine capacity: leases=%d", live)
						}
						input, claimed := claims[id]
						if !claimed {
							_, input = claimRemote(t, f, id)
						}
						if err := f.report(ctx, input, store.NodeSuccess); err != nil {
							t.Fatal(err)
						}
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if live, granted, released := d.counts(); live != 0 || granted != released || granted != machineLeases {
						t.Fatalf("leases=%d/%d/%d", live, granted, released)
					}
					state, err := f.Tenant.GetConcurrencyState(ctx, scopedConcurrencyKey("db", scope, f.Input.RunID, testBox))
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
	configureNativeAdmission(&f.Host, &LocalAdmission{Daemon: d, BoxID: testBox}, &storeAdmission{Store: f.Tenant, Owner: f.Actor, BoxID: testBox}, false)
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
	replacement := newStoreHostFixtureOn(t, f.Store, plan, runspec.Options{})
	admission := storeAdmission{Store: replacement.Tenant, Owner: replacement.Actor}
	if err := admission.Node(ctx, replacement.Accepted, capacityRow(t, replacement, "build"), func(context.Context) error { return nil }); err != nil {
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
	if live, _, _ := d.counts(); live != 1 {
		t.Fatal("cancellation released the owner lease before the remote holder drained")
	}
	if err := f.report(ctx, input, store.NodeCancelled); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if live, granted, released := d.counts(); live != 0 || granted != released {
		t.Fatalf("leases=%d/%d/%d", live, granted, released)
	}
}

func FuzzRemoteAdmissionPreservesJobIdentity(f *testing.F) {
	f.Add("run", "job", "spec", int64(3), int64(4), true)
	f.Add("foreign", "job", "spec", int64(3), int64(4), true)
	f.Add("run", "job", "spec", int64(2), int64(4), true)
	f.Add("run", "job", "spec", int64(3), int64(3), true)
	f.Add("run", "job", "spec", int64(3), int64(4), false)
	f.Fuzz(func(t *testing.T, run, job, hash string, generation, sequence int64, terminal bool) {
		before := store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, SpecHash: "spec", Generation: 3, StateSeq: 4}
		row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: run, NodeID: job}, SpecHash: hash, Generation: generation, StateSeq: sequence, Status: store.StatusPending}
		if terminal {
			row.Status, row.Outcome = store.StatusDone, store.NodeSuccess
		}
		s := &scriptedHostStore{readSnapshot: func(context.Context, string) (store.RunSnapshot, error) {
			return store.RunSnapshot{Run: store.RunRow{ID: run}, Nodes: []store.NodeRow{row}}, nil
		}}
		host := OwnerHost{Store: s, Wait: func(context.Context, store.RunSnapshot) error { return errors.New("not terminal") }}
		err := host.followRemote(t.Context(), t.Context(), store.Actor{}, before)
		valid := run == before.Ref.RunID && job == before.Ref.NodeID && hash == before.SpecHash && generation >= before.Generation && sequence >= before.StateSeq && terminal
		if (err == nil) != valid {
			t.Fatalf("foreign, regressed or unfinished job settled: valid=%v error=%v", valid, err)
		}
	})
}
