package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	wingdserver "github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func reservationSummary(cores float64) store.ExecutorSchedulingSummary {
	return store.ExecutorSchedulingSummary{
		RunID: "run-1", NodeID: "node-1", ResourceDigest: "sha256:resource-1", Slots: 1,
		Resources: store.ExecutorResource{Cores: cores},
	}
}

func reservationMembership() store.ExecutorMembershipSnapshot {
	return store.ExecutorMembershipSnapshot{MembershipID: "membership-a", WorkerID: "worker-a", Kind: "agent", Eligible: true, MaxConcurrent: 2}
}

func TestWingdExecutorCapacityLedger_ReserveConsumeReleaseLifecycle(t *testing.T) {
	home := t.TempDir()
	startE2EDaemon(t, home, 2)
	ledger := NewWingdExecutorCapacityLedger(home, "v1")

	r, err := ledger.Reserve(context.Background(), reservationSummary(1), reservationMembership(), 0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if r.ID() == "" || r.MembershipID() != "membership-a" || r.WorkerID() != "worker-a" ||
		r.RunID() != "run-1" || r.NodeID() != "node-1" || r.ResourceDigest() != "sha256:resource-1" || r.Slot() != 0 {
		t.Fatalf("reservation identity = %q %q %d", r.ID(), r.WorkerID(), r.Slot())
	}
	if _, err := ledger.Reserve(context.Background(), reservationSummary(1), reservationMembership(), 0); !errors.Is(err, ErrExecutorCapacityUnavailable) {
		t.Fatalf("duplicate slot = %v, want ErrExecutorCapacityUnavailable", err)
	}
	otherMembership := reservationMembership()
	otherMembership.MembershipID = "membership-b"
	otherMembership.WorkerID = "worker-name-at-another-coordinator"
	if _, err := ledger.Reserve(context.Background(), reservationSummary(1), otherMembership, 0); !errors.Is(err, ErrExecutorCapacityUnavailable) {
		t.Fatalf("same physical slot through another membership = %v, want ErrExecutorCapacityUnavailable", err)
	}
	admission, err := r.Consume()
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if admission.ParentLeaseToken == "" || admission.Origin != wingwire.OriginController {
		t.Fatalf("consumed admission = %+v", admission)
	}
	if _, err := r.Consume(); err == nil {
		t.Fatal("second Consume succeeded")
	}
	if err := r.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := r.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	reused, err := ledger.Reserve(context.Background(), reservationSummary(1), reservationMembership(), 0)
	if err != nil {
		t.Fatalf("Reserve reused slot: %v", err)
	}
	t.Cleanup(func() { _ = reused.Release() })
}

func TestWingdExecutorCapacityLedger_RejectsInvalidIdentityDigestAndSlot(t *testing.T) {
	ledger := NewWingdExecutorCapacityLedger(t.TempDir(), "v1")
	summary := reservationSummary(1)
	membership := reservationMembership()
	for _, tc := range []struct {
		name       string
		summary    store.ExecutorSchedulingSummary
		membership store.ExecutorMembershipSnapshot
		slot       int
	}{
		{name: "missing digest", summary: func() store.ExecutorSchedulingSummary { s := summary; s.ResourceDigest = ""; return s }(), membership: membership, slot: 0},
		{name: "negative slot", summary: summary, membership: membership, slot: -1},
		{name: "slot at ceiling", summary: summary, membership: membership, slot: membership.MaxConcurrent},
		{name: "missing membership", summary: summary, membership: store.ExecutorMembershipSnapshot{Eligible: true, WorkerID: "worker-a", MaxConcurrent: 1}, slot: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ledger.Reserve(context.Background(), tc.summary, tc.membership, tc.slot); err == nil {
				t.Fatal("invalid reservation succeeded")
			}
		})
	}
}

func TestWingdExecutorCapacityReservation_ConsumeFailsAfterDaemonLoss(t *testing.T) {
	home := t.TempDir()
	daemon, err := wingdserver.New(wingdserver.Config{
		Home: home, Version: "v1", GraceWindow: -1, HeadroomFraction: -1,
		Sampler: e2eSampler{cores: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	daemonCtx, stopDaemon := context.WithCancel(context.Background())
	go func() { _ = daemon.Run(daemonCtx) }()
	select {
	case <-daemon.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("daemon never became ready")
	}
	ledger := NewWingdExecutorCapacityLedger(home, "v1")
	reservation, err := ledger.Reserve(context.Background(), reservationSummary(1), reservationMembership(), 0)
	if err != nil {
		t.Fatal(err)
	}
	stopDaemon()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err = wingdclient.Query(ctx, wingdclient.Options{Home: home, Version: "v1"})
		cancel()
		if err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err == nil {
		t.Fatal("daemon remained reachable after shutdown")
	}
	if _, err := reservation.Consume(); err == nil {
		t.Fatal("stale reservation consumed after daemon loss")
	}
}

func TestWingdExecutorCapacityLedger_RefusesWithoutQueueing(t *testing.T) {
	home := t.TempDir()
	startE2EDaemon(t, home, 2)
	cl, err := wingdclient.EnsureDaemon(context.Background(), wingdclient.Options{Home: home, Version: "v1"})
	if err != nil {
		t.Fatalf("EnsureDaemon: %v", err)
	}
	holder, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID: "local-holder", Resources: wingwire.HostResources{Cores: 2}, CostSource: wingwire.CostSourcePin,
	}, nil)
	if err != nil {
		t.Fatalf("hold capacity: %v", err)
	}
	t.Cleanup(func() { _ = holder.Release() })

	ledger := NewWingdExecutorCapacityLedger(home, "v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := ledger.Reserve(ctx, reservationSummary(1), reservationMembership(), 0); !errors.Is(err, ErrExecutorCapacityUnavailable) {
		t.Fatalf("Reserve under pressure = %v, want ErrExecutorCapacityUnavailable", err)
	}
	state, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "v1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(state.Waiters) != 0 {
		t.Fatalf("nonblocking reservation left %d waiter(s)", len(state.Waiters))
	}
}
