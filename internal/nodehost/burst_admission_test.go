package nodehost

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func burstAdmissionFixture(t *testing.T) (*ownerFixture, OwnerHost, RunInput, *fakeDaemon) {
	t.Helper()
	f, host, input := newOwnerFixture(t, true)
	spec := f.snapshot.Run.Spec
	spec.Request.Request.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}, {Target: "cloud"}}}
	spec.Resolved.Settings.Options.Resources.Value.Cores.Value = 4
	f.snapshot.Run.Request = spec.Request
	input.SealedRequest, _ = json.Marshal(spec.Request.Request)
	accepted, err := ownerAccepted(f.snapshot.Run)
	if err != nil {
		t.Fatal(err)
	}
	input.Accepted = &accepted
	f.snapshot.Nodes = []store.NodeRow{{Ref: store.NodeRef{RunID: input.RunID, NodeID: "job"}, Status: store.StatusPending, SpecHash: "job-hash"}}
	f.snapshot.Nodes[0].Execution.Resources.Value.Cores.Value = 4
	host.Local = func(store.NodeRow) bool { return true }
	host.Layer = func(context.Context, LayerSpec, EvaluationInput, store.NodeRow, StorageBinding) (OwnerLayerResult, error) {
		return OwnerLayerResult{}, nil
	}
	d := newFakeDaemon()
	configureNativeAdmission(&host, &LocalAdmission{Daemon: d, BoxID: testBox}, nil)
	return f, host, input, d
}

func TestBurstReleasesReadyWorkWithoutMachineCapacity(t *testing.T) {
	f, host, input, daemon := burstAdmissionFixture(t)
	daemon.hostGate = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := host.Around(ctx, *input.Accepted, func(ctx context.Context) (RunCompletion, error) {
			return RunCompletion{}, host.release(ctx, input, *input.Accepted, &source.Entry{}, f.snapshot.Nodes[0], store.DecisionReady, func(context.Context, store.ReconcileRequest) error {
				row, err := f.ReadNode(ctx, f.snapshot.Nodes[0].Ref)
				if err == nil && row.ReadyAt == nil {
					err = errors.New("burst job was not released")
				}
				return err
			})
		})
		done <- err
	}()
	select {
	case <-daemon.queued:
		cancel()
		<-done
		t.Fatal("machine contention blocked ready work and overflow")
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range daemon.requests() {
		if !request.SemaphoresOnly || request.Resources.Cores != 0 {
			t.Fatalf("unclaimed burst retained machine resources: %+v", request)
		}
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != released {
		t.Fatalf("release leaked admission: %d/%d/%d", live, granted, released)
	}
}

func TestBurstSelfClaimHoldsMachineCapacityThroughExecution(t *testing.T) {
	f, host, input, daemon := burstAdmissionFixture(t)
	host.Executor = ownerNodeFunc(func(ctx context.Context, node NodeInput) error {
		requests := daemon.requests()
		if live, _, _ := daemon.counts(); live != 1 || len(requests) != 1 || requests[0].Resources.Cores != 4 || requests[0].SemaphoresOnly {
			t.Fatalf("self claim started without job capacity: %+v live=%d", requests, live)
		}
		f.record("execute")
		return nil
	})
	err := host.execute(t.Context(), input, *input.Accepted, &source.Entry{Engine: engine.Resolved{Identity: f.snapshot.Run.Spec.Resolved.Engine}}, store.ReconcileRequest{Ref: f.snapshot.Nodes[0].Ref, Actor: input.Owner})
	if err != nil {
		t.Fatal(err)
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 1 || released != 1 {
		t.Fatalf("execution leaked admission: %d/%d/%d", live, granted, released)
	}
}

func TestBurstSelfClaimCancelledDuringMachineContentionNeverStarts(t *testing.T) {
	f, host, input, daemon := burstAdmissionFixture(t)
	daemon.hostGate = make(chan struct{})
	host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error { t.Error("queued self claim started execution"); return nil })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- host.execute(ctx, input, *input.Accepted, &source.Entry{Engine: engine.Resolved{Identity: f.snapshot.Run.Spec.Resolved.Engine}}, store.ReconcileRequest{Ref: f.snapshot.Nodes[0].Ref, Actor: input.Owner})
	}()
	select {
	case <-daemon.queued:
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("queue cancellation lost: %v", err)
		}
	case err := <-done:
		t.Fatalf("self claim bypassed contended admission: %v", err)
	}
	if live, _, _ := daemon.counts(); live != 0 {
		t.Fatal("cancelled self claim retained machine capacity")
	}
}

func TestBurstConcurrencyIsHeldOnceAcrossSelfClaim(t *testing.T) {
	f, host, input, daemon := burstAdmissionFixture(t)
	row := f.snapshot.Nodes[0]
	row.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Scope: "run", Capacity: 1, Cost: 1, OnLimit: "fail"}}
	f.snapshot.Nodes[0] = row
	host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error {
		requests := daemon.requests()
		if live, _, _ := daemon.counts(); live != 3 || len(requests) != 3 || len(requests[1].Semaphores) != 1 || len(requests[2].Semaphores) != 0 {
			t.Fatalf("burst reacquired or lost concurrency: requests=%+v live=%d", requests, live)
		}
		return nil
	})
	_, err := host.Around(t.Context(), *input.Accepted, func(ctx context.Context) (RunCompletion, error) {
		return RunCompletion{}, host.release(ctx, input, *input.Accepted, &source.Entry{}, row, store.DecisionReady, func(ctx context.Context, request store.ReconcileRequest) error {
			return host.execute(ctx, input, *input.Accepted, &source.Entry{Engine: engine.Resolved{Identity: f.snapshot.Run.Spec.Resolved.Engine}}, request)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 3 || released != 3 {
		t.Fatalf("burst concurrency leaked: %d/%d/%d", live, granted, released)
	}
}

func TestBurstSelfClaimWaitsForCompetingLocalWorkThenExecutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, host, input, daemon := burstAdmissionFixture(t)
		daemon.hostGate = make(chan struct{})
		executed := false
		host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error { executed = true; return nil })
		done := make(chan error, 1)
		go func() {
			done <- host.execute(t.Context(), input, *input.Accepted, &source.Entry{Engine: engine.Resolved{Identity: f.snapshot.Run.Spec.Resolved.Engine}}, store.ReconcileRequest{Ref: f.snapshot.Nodes[0].Ref, Actor: input.Owner})
		}()
		synctest.Wait()
		if executed || len(daemon.queued) != 1 {
			t.Fatal("self claim bypassed competing machine work")
		}
		close(daemon.hostGate)
		if err := <-done; err != nil || !executed {
			t.Fatalf("capacity returning did not unblock execution: %v", err)
		}
		if live, granted, released := daemon.counts(); live != 0 || granted != 1 || released != 1 {
			t.Fatalf("resumed self claim leaked admission: %d/%d/%d", live, granted, released)
		}
	})
}

func TestBurstRegisteredClaimDoesNotReacquireRequesterConcurrency(t *testing.T) {
	burstSharedClaimConcurrency(t, "run", "db", 1, 1)
}

func FuzzBurstClaimChargesRequesterConcurrencyOnce(f *testing.F) {
	f.Add([]byte("db"), uint8(1), uint8(1), false)
	f.Add([]byte("path:/db"), uint8(3), uint8(2), true)
	f.Fuzz(func(t *testing.T, name []byte, size, demand uint8, box bool) {
		if len(name) > 256 {
			return
		}
		scope := "run"
		if box {
			scope = "box"
		}
		capacity := 1 + int(size)%8
		burstSharedClaimConcurrency(t, scope, "db-"+hex.EncodeToString(name), capacity, 1+int(demand)%capacity)
	})
}

func burstSharedClaimConcurrency(t *testing.T, scope, group string, capacity, cost int) {
	t.Helper()
	f, host, input, daemon := burstAdmissionFixture(t)
	row := f.snapshot.Nodes[0]
	row.Execution.Concurrency = []runspec.Concurrency{{Group: group, Scope: scope, Capacity: capacity, Cost: cost, OnLimit: "fail"}}
	_, err := host.Around(t.Context(), *input.Accepted, func(ctx context.Context) (RunCompletion, error) {
		return RunCompletion{}, host.Admit(ctx, *input.Accepted, row, func(ctx context.Context) error {
			return (&LocalAdmission{Daemon: daemon}).ClaimedNode(ctx, *input.Accepted, row, func(context.Context) error {
				requests := daemon.requests()
				if len(requests) != 3 || len(requests[1].Semaphores) != 1 || len(requests[2].Semaphores) != 0 || requests[2].Resources.Cores != 4 {
					t.Fatalf("registered claim did not retain requester's concurrency: %+v", requests)
				}
				return nil
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if live, granted, released := daemon.counts(); live != 0 || granted != 3 || released != 3 {
		t.Fatalf("registered claim leaked admission: %d/%d/%d", live, granted, released)
	}
}

func FuzzBurstReleaseNeverReservesMachineResources(f *testing.F) {
	f.Add(uint8(4), uint32(1024), true)
	f.Add(uint8(0), uint32(0), false)
	f.Fuzz(func(t *testing.T, cores uint8, memory uint32, grouped bool) {
		_, host, input, daemon := burstAdmissionFixture(t)
		var spec runspec.Spec
		if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
			t.Fatal(err)
		}
		spec.Resolved.Settings.Options.Resources.Value.Cores.Value = float64(cores)
		spec.Resolved.Settings.Options.Resources.Value.MemoryBytes.Value = int64(memory)
		row := store.NodeRow{Ref: store.NodeRef{RunID: input.RunID, NodeID: "job"}}
		row.Execution.Resources = spec.Resolved.Settings.Options.Resources
		if grouped {
			row.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Scope: "run", Capacity: 1, Cost: 1, OnLimit: "fail"}}
		}
		input.Accepted.Spec, _ = json.Marshal(spec)
		_, err := host.Around(t.Context(), *input.Accepted, func(ctx context.Context) (RunCompletion, error) {
			return RunCompletion{}, host.Admit(ctx, *input.Accepted, row, func(context.Context) error { return nil })
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, request := range daemon.requests() {
			if !request.SemaphoresOnly || request.Resources != (wingwire.HostResources{}) {
				t.Fatalf("unclaimed burst occupied machine capacity: %+v", request)
			}
		}
		if live, granted, released := daemon.counts(); live != 0 || granted != released {
			t.Fatalf("unclaimed admission leaked: %d/%d/%d", live, granted, released)
		}
	})
}

type burstPlacementStore struct{ *ownerFixture }

func (s burstPlacementStore) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	row, err := s.ReadNode(ctx, request.Ref)
	if err != nil || row.OwnerExecution == nil || !row.OwnerExecution.Allowed {
		return store.ReconciledNode{Kind: store.Dispatch, Node: row}, err
	}
	return s.ownerFixture.ReconcileNode(ctx, request)
}

func TestBurstRetainsConcurrencyWhileWaitingForPlacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, host, input, daemon := burstAdmissionFixture(t)
		host.Store = burstPlacementStore{f}
		row := f.snapshot.Nodes[0]
		row.Execution.Concurrency = []runspec.Concurrency{{Group: "db", Scope: "run", Capacity: 1, Cost: 1, OnLimit: "fail"}}
		f.snapshot.Nodes[0] = row
		executed := false
		host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error { executed = true; return nil })
		done := make(chan error, 1)
		go func() {
			_, err := host.Around(t.Context(), *input.Accepted, func(ctx context.Context) (RunCompletion, error) {
				return RunCompletion{}, host.release(ctx, input, *input.Accepted, &source.Entry{}, row, store.DecisionReady, func(ctx context.Context, request store.ReconcileRequest) error {
					return host.execute(ctx, input, *input.Accepted, &source.Entry{Engine: engine.Resolved{Identity: f.snapshot.Run.Spec.Resolved.Engine}}, request)
				})
			})
			done <- err
		}()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("burst dropped concurrency before its placement settled: %v", err)
		default:
		}
		if live, _, _ := daemon.counts(); live != 2 || executed {
			t.Fatalf("waiting burst lost concurrency or used compute: live=%d executed=%v", live, executed)
		}
		f.mu.Lock()
		f.snapshot.Nodes[0].OwnerExecution = &store.OwnerExecution{Allowed: true}
		f.mu.Unlock()
		f.changed <- struct{}{}
		if err := <-done; err != nil || !executed {
			t.Fatalf("returned capacity did not resume the waiting owner: executed=%v error=%v", executed, err)
		}
		if live, granted, released := daemon.counts(); live != 0 || granted != 3 || released != 3 {
			t.Fatalf("placement wait leaked admission: %d/%d/%d", live, granted, released)
		}
	})
}
