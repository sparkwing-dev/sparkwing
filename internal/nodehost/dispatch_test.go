package nodehost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
	claim, err := f.Store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: f.Input.RunID, NodeID: node}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}})
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
	if _, err := f.Store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: f.Control.CapabilityPrefix, Target: &store.UnitRef{Kind: capability.UnitNode, RunID: ref.RunID, NodeID: ref.NodeID}, Available: match.Resources{Cores: 64, MemoryBytes: 64 << 30}}); err == nil {
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

func TestDispatchRetainsLocalSemaphoresUntilRemoteCompletion(t *testing.T) {
	for _, scope := range []string{"run", "box"} {
		for _, mode := range []string{"fresh", "ready", "claimed"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
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
				configureNativeAdmission(&f.Host, &LocalAdmission{Daemon: d, BoxID: testBox}, &StoreAdmission{Store: f.Tenant, Owner: f.Actor})
				d.admitted = make(chan wingwire.AdmissionRequest, 3)
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
					case request := <-d.admitted:
						if string(request.RunID) == f.Input.RunID {
							request = <-d.admitted
						}
						if request.RunID != wingwire.NodeSemaphoreParticipant(f.Input.RunID, id) {
							t.Fatalf("unexpected remote admission: %+v", request)
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
					if live, _, _ := d.counts(); live != 2 {
						t.Fatalf("ready remote job lost its semaphore: leases=%d", live)
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
				if live, granted, released := d.counts(); live != 0 || granted != 3 || released != 3 {
					t.Fatalf("leases=%d/%d/%d", live, granted, released)
				}
				requests := d.requests()
				for _, req := range requests[1:] {
					if !req.SemaphoresOnly || req.Resources != (wingwire.HostResources{}) || !req.SubLease || req.OwnerRunID != f.Input.RunID || req.OwnerLeaseToken != "lease-"+f.Input.RunID || len(req.Semaphores) != 1 || req.Semaphores[0].Name != scopedConcurrencyKey("db", scope, f.Input.RunID, testBox) {
						t.Fatalf("remote work reserved local resources or lost its binding: %+v", req)
					}
				}
			})
		}
	}
}

func TestDispatchAdmissionEvictionDrainsTheRemoteHolder(t *testing.T) {
	plan := remotePlan()
	plan.Jobs = plan.Jobs[:1]
	plan.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "db", Scope: new("box"), OnLimit: new("cancel_others")}}
	f := newStoreHostFixture(t, plan)
	f.Host.Local = func(store.NodeRow) bool { return false }
	d := newFakeDaemon()
	configureNativeAdmission(&f.Host, &LocalAdmission{Daemon: d, BoxID: testBox}, &StoreAdmission{Store: f.Tenant, Owner: f.Actor})
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
	d.mu.Lock()
	held := d.leases[string(wingwire.NodeSemaphoreParticipant(f.Input.RunID, "build"))]
	d.mu.Unlock()
	held.evict <- wingwire.Evicted{Key: "db", SupersededBy: "newer"}
	for {
		snapshot, err := f.Tenant.ReadSnapshot(ctx, f.Input.RunID)
		if err != nil {
			t.Fatal(err)
		}
		row, _ := ownerNode(snapshot, "build")
		if row.CancelRequestedAt != nil {
			if row.Status == store.StatusDone || row.ClaimedBy != input.Authority.Holder || row.Generation != input.Generation {
				t.Fatal("eviction replaced the holder's result authority")
			}
			break
		}
		if err := f.Host.Wait(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if live, _, _ := d.counts(); live != 2 {
		t.Fatal("eviction released capacity before the remote holder drained")
	}
	if err := f.report(ctx, input, store.NodeCancelled); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("eviction lost its cause: %v", err)
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
