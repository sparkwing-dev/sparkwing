package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type reservedNodeGate struct {
	started chan struct{}
	release chan struct{}
}

type reservedNodeResult struct {
	outcome sparkwing.Outcome
	err     error
}

var (
	reservedNodeGatePtr          atomic.Pointer[reservedNodeGate]
	registerReservedNodePipeline sync.Once
)

type reservedNodePipeline struct{ sparkwing.Base }

func (reservedNodePipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	gate := reservedNodeGatePtr.Load()
	sparkwing.Job(plan, "work", func(ctx context.Context) error {
		close(gate.started)
		select {
		case <-gate.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}).Resources(sparkwing.Cores(1))
	return nil
}

func registerReservedAdmissionPipeline() {
	registerReservedNodePipeline.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("reserved-node-admission", func() sparkwing.Pipeline[sparkwing.NoInputs] {
			return reservedNodePipeline{}
		})
	})
}

func startReservedNodeExecution(t *testing.T, ctx context.Context, home string, admission *orchestrator.LocalAdmission) (*reservedNodeGate, <-chan reservedNodeResult) {
	t.Helper()
	t.Setenv("SPARKWING_HOME", home)
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).Handler())
	t.Cleanup(srv.Close)

	const runID = "reserved-run"
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "reserved-node-admission", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: "work", Status: "pending"}); err != nil {
		t.Fatal(err)
	}

	gate := &reservedNodeGate{started: make(chan struct{}), release: make(chan struct{})}
	reservedNodeGatePtr.Store(gate)
	t.Cleanup(func() { reservedNodeGatePtr.Store(nil) })
	result := make(chan reservedNodeResult, 1)
	go func() {
		res, runErr := orchestrator.RunNodeOnce(ctx, srv.URL, "", runID, "work",
			"executor:workstation:0", "", nil, quiet, admission)
		result <- reservedNodeResult{outcome: res.Outcome, err: runErr}
	}()
	return gate, result
}

func awaitReservedNodeStart(t *testing.T, gate *reservedNodeGate) {
	t.Helper()
	select {
	case <-gate.started:
	case <-time.After(3 * time.Second):
		t.Fatal("node body did not start inside the pre-admitted one-core reservation")
	}
}

func reservationSummary(cores float64) store.ExecutorSchedulingSummary {
	return store.ExecutorSchedulingSummary{
		RunID: "run-1", NodeID: "node-1", ResourceDigest: "sha256:resource-1", Slots: 1,
		Resources: store.ExecutorResource{Cores: cores},
	}
}

func reservationMembership() store.ExecutorMembershipSnapshot {
	return store.ExecutorMembershipSnapshot{MembershipID: "membership-a", WorkerID: "worker-a", Kind: "agent", Eligible: true, MaxConcurrent: 2}
}

func TestExecutorCapacitySemaphoresRoundChargesConservatively(t *testing.T) {
	claims, ok := executorCapacitySemaphores(
		qs(8, 8, 16<<30, 16<<30, 0),
		store.ExecutorResource{Cores: 0.0001, MemoryBytes: 1},
		"membership-a",
		executorCapacityLimits{},
		0,
	)
	if !ok {
		t.Fatal("fractional resource charge was rejected")
	}
	var sawCores, sawMemory bool
	for _, claim := range claims {
		if claim.Name == "sparkwing/executor/global/cores" {
			sawCores = true
			if claim.Cost != 1 {
				t.Errorf("fractional core cost = %d, want conservative one millicore", claim.Cost)
			}
		}
		if claim.Name == "sparkwing/executor/global/memory" {
			sawMemory = true
			if claim.Cost != 1 {
				t.Errorf("fractional memory cost = %d, want conservative one MiB unit", claim.Cost)
			}
		}
	}
	if !sawCores || !sawMemory {
		t.Fatalf("capacity claims missing global resources: %+v", claims)
	}
}

func TestWingdExecutorCapacityLedger_ReserveConsumeReleaseLifecycle(t *testing.T) {
	home := t.TempDir()
	startE2EDaemon(t, home, 2)
	ledger := NewWingdExecutorCapacityLedger(home, "v1")

	r, err := ledger.Reserve(context.Background(), reservationSummary(1), reservationMembership(), executorCapacityLimits{}, 0)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if r.ID() == "" || r.MembershipID() != "membership-a" || r.WorkerID() != "worker-a" ||
		r.RunID() != "run-1" || r.NodeID() != "node-1" || r.ResourceDigest() != "sha256:resource-1" || r.Slot() != 0 {
		t.Fatalf("reservation identity = %q %q %d", r.ID(), r.WorkerID(), r.Slot())
	}
	if _, err := ledger.Reserve(context.Background(), reservationSummary(1), reservationMembership(), executorCapacityLimits{}, 0); !errors.Is(err, ErrExecutorCapacityUnavailable) {
		t.Fatalf("duplicate slot = %v, want ErrExecutorCapacityUnavailable", err)
	}
	otherMembership := reservationMembership()
	otherMembership.MembershipID = "membership-b"
	otherMembership.WorkerID = "worker-name-at-another-coordinator"
	if _, err := ledger.Reserve(context.Background(), reservationSummary(1), otherMembership, executorCapacityLimits{}, 0); !errors.Is(err, ErrExecutorCapacityUnavailable) {
		t.Fatalf("same physical slot through another membership = %v, want ErrExecutorCapacityUnavailable", err)
	}
	admission, err := r.Consume()
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if admission == nil || admission.Origin != wingwire.OriginController {
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
	reused, err := ledger.Reserve(context.Background(), reservationSummary(1), reservationMembership(), executorCapacityLimits{}, 0)
	if err != nil {
		t.Fatalf("Reserve reused slot: %v", err)
	}
	t.Cleanup(func() { _ = reused.Release() })
}

func TestWingdExecutorCapacityLedger_EnforcesMachineAndMembershipCeilingsAcrossConcurrentLedgers(t *testing.T) {
	for _, test := range []struct {
		name       string
		limits     func(string) executorCapacityLimits
		resources  store.ExecutorResource
		membership []string
		wantTotal  int
		maxByID    map[string]int
	}{
		{
			name: "global contribution and local reserve",
			limits: func(string) executorCapacityLimits {
				return executorCapacityLimits{
					localReserve:           reserve{cores: 2, memoryBytes: 4 << 30},
					globalContribution:     reserve{cores: 4, memoryBytes: 8 << 30},
					membershipContribution: reserve{cores: 8, memoryBytes: 16 << 30},
				}
			},
			resources:  store.ExecutorResource{Cores: 1, MemoryBytes: 2 << 30},
			membership: []string{"a", "b", "a", "b", "a", "b"},
			wantTotal:  4,
		},
		{
			name: "local reserve is the default contribution ceiling",
			limits: func(string) executorCapacityLimits {
				return executorCapacityLimits{localReserve: reserve{cores: 6, memoryBytes: 12 << 30}}
			},
			resources:  store.ExecutorResource{Cores: 1, MemoryBytes: 2 << 30},
			membership: []string{"a", "b", "a", "b"},
			wantTotal:  2,
		},
		{
			name: "each membership has its own narrower ceiling",
			limits: func(id string) executorCapacityLimits {
				cores, memory := 2.0, int64(4<<30)
				if id == "b" {
					cores, memory = 3, 6<<30
				}
				return executorCapacityLimits{
					globalContribution:     reserve{cores: 6, memoryBytes: 12 << 30},
					membershipContribution: reserve{cores: cores, memoryBytes: memory},
				}
			},
			resources:  store.ExecutorResource{Cores: 1, MemoryBytes: 2 << 30},
			membership: []string{"a", "a", "a", "a", "b", "b", "b", "b"},
			wantTotal:  5,
			maxByID:    map[string]int{"a": 2, "b": 3},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, err := os.MkdirTemp("/tmp", "sparkwing-executor-capacity-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			startE2EDaemon(t, home, 8)
			ledgers := []*WingdExecutorCapacityLedger{
				NewWingdExecutorCapacityLedger(home, "v1"),
				NewWingdExecutorCapacityLedger(home, "v1"),
			}
			start := make(chan struct{})
			results := make(chan struct {
				membership  string
				reservation ExecutorCapacityReservation
				err         error
			}, len(test.membership))
			for slot, membershipID := range test.membership {
				slot, membershipID := slot, membershipID
				go func() {
					<-start
					summary := reservationSummary(test.resources.Cores)
					summary.RunID = fmt.Sprintf("run-%d", slot)
					summary.NodeID = fmt.Sprintf("node-%d", slot)
					summary.ResourceDigest = fmt.Sprintf("sha256:resource-%d", slot)
					summary.Resources = test.resources
					membership := reservationMembership()
					membership.MembershipID = "membership-" + membershipID
					membership.WorkerID = "worker-" + membershipID
					membership.MaxConcurrent = len(test.membership)
					reservation, err := ledgers[slot%len(ledgers)].Reserve(
						context.Background(), summary, membership, test.limits(membershipID), slot,
					)
					results <- struct {
						membership  string
						reservation ExecutorCapacityReservation
						err         error
					}{membershipID, reservation, err}
				}()
			}
			close(start)
			gotByID := map[string]int{}
			var held []ExecutorCapacityReservation
			for range test.membership {
				result := <-results
				if result.err == nil {
					gotByID[result.membership]++
					held = append(held, result.reservation)
					continue
				}
				if !errors.Is(result.err, ErrExecutorCapacityUnavailable) {
					t.Errorf("Reserve = %v, want capacity unavailable", result.err)
				}
			}
			if len(held) != test.wantTotal {
				t.Errorf("reservations = %d, want %d", len(held), test.wantTotal)
			}
			for id, limit := range test.maxByID {
				if gotByID[id] > limit {
					t.Errorf("membership %s reservations = %d, exceeds %d", id, gotByID[id], limit)
				}
			}
			for _, reservation := range held {
				if err := reservation.Release(); err != nil {
					t.Errorf("Release: %v", err)
				}
			}
		})
	}
}

func TestWingdExecutorCapacityLedger_FencesPhysicalSlotAcrossLedgerInstances(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "sparkwing-executor-slot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	startE2EDaemon(t, home, 2)
	ledgers := []*WingdExecutorCapacityLedger{
		NewWingdExecutorCapacityLedger(home, "v1"),
		NewWingdExecutorCapacityLedger(home, "v1"),
	}
	start := make(chan struct{})
	results := make(chan struct {
		reservation ExecutorCapacityReservation
		err         error
	}, len(ledgers))
	for _, ledger := range ledgers {
		ledger := ledger
		go func() {
			<-start
			reservation, err := ledger.Reserve(
				context.Background(), reservationSummary(1), reservationMembership(), executorCapacityLimits{}, 0,
			)
			results <- struct {
				reservation ExecutorCapacityReservation
				err         error
			}{reservation, err}
		}()
	}
	close(start)
	winners := 0
	var held []ExecutorCapacityReservation
	for range ledgers {
		result := <-results
		if result.err == nil {
			winners++
			held = append(held, result.reservation)
		} else if !errors.Is(result.err, ErrExecutorCapacityUnavailable) {
			t.Fatalf("Reserve = %v, want capacity unavailable", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("same-slot winners = %d, want 1", winners)
	}
	for _, reservation := range held {
		if err := reservation.Release(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWingdExecutorCapacityReservation_RunsInsideTheReservedLease(t *testing.T) {
	registerReservedAdmissionPipeline()
	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{name: "finish"},
		{name: "cancel", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			startE2EDaemon(t, home, 1)

			ctx := context.Background()
			const runID = "reserved-run"
			summary := reservationSummary(1)
			summary.RunID = runID
			summary.NodeID = "work"
			ledger := NewWingdExecutorCapacityLedger(home, "v1")
			reservation, err := ledger.Reserve(ctx, summary, reservationMembership(), executorCapacityLimits{}, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reservation.Release() })
			admission, err := reservation.Consume()
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancelRun := context.WithCancel(ctx)
			t.Cleanup(cancelRun)
			execCtx, cancelExec := reservation.ExecutionContext(runCtx)
			t.Cleanup(cancelExec)
			gate, result := startReservedNodeExecution(t, execCtx, home, admission)
			awaitReservedNodeStart(t, gate)
			assertOnlyReservationHolder(t, home, reservation.ID())

			if tc.cancel {
				cancelRun()
			} else {
				close(gate.release)
			}
			select {
			case got := <-result:
				if got.err != nil {
					t.Fatalf("RunNodeOnce: %v", got.err)
				}
				if tc.cancel {
					if got.outcome == sparkwing.Success {
						t.Fatal("cancelled node reported success")
					}
				} else if got.outcome != sparkwing.Success {
					t.Fatalf("node outcome = %q, want success", got.outcome)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("RunNodeOnce did not return")
			}

			// safety: only the outer offer-slot cleanup owns the reservation lifetime.
			assertOnlyReservationHolder(t, home, reservation.ID())
			if err := reservation.Release(); err != nil {
				t.Fatal(err)
			}
			assertNoReservationHolders(t, home)
			if err := reservation.Release(); err != nil {
				t.Fatalf("idempotent release: %v", err)
			}
		})
	}
}

func TestWingdExecutorCapacityReservation_ReattachesAcrossRestart(t *testing.T) {
	registerReservedAdmissionPipeline()
	home := t.TempDir()
	const grace = 300 * time.Millisecond
	first := startE2EDaemonConfig(t, wingd.Config{
		Home: home, Version: "v1", GraceWindow: grace, HeadroomFraction: -1,
		Sampler: e2eSampler{cores: 1},
	})

	spawnRequested := make(chan struct{})
	spawnReady := make(chan struct{})
	var requestOnce sync.Once
	ledger := NewWingdExecutorCapacityLedger(home, "v1")
	ledger.spawn = func(string, string) error {
		requestOnce.Do(func() { close(spawnRequested) })
		<-spawnReady
		return nil
	}
	summary := reservationSummary(1)
	summary.RunID = "reserved-run"
	summary.NodeID = "work"
	reservation, err := ledger.Reserve(context.Background(), summary, reservationMembership(), executorCapacityLimits{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reservation.Release() })
	admission, err := reservation.Consume()
	if err != nil {
		t.Fatal(err)
	}
	execCtx, cancelExec := reservation.ExecutionContext(context.Background())
	t.Cleanup(cancelExec)
	gate, result := startReservedNodeExecution(t, execCtx, home, admission)
	awaitReservedNodeStart(t, gate)
	assertOnlyReservationHolder(t, home, reservation.ID())

	if err := first.waitExit(t); err != nil {
		t.Fatalf("first daemon exit: %v", err)
	}
	select {
	case <-spawnRequested:
	case <-time.After(3 * time.Second):
		t.Fatal("reservation watcher did not attempt recovery")
	}
	startE2EDaemonConfig(t, wingd.Config{
		Home: home, Version: "v1", GraceWindow: grace, HeadroomFraction: -1,
		Sampler: e2eSampler{cores: 1},
	})
	close(spawnReady)
	waitForOnlyReservationHolder(t, home, reservation.ID())
	time.Sleep(2 * grace)
	assertOnlyReservationHolder(t, home, reservation.ID())

	if competitor, err := acquireNonblockingCore(home, "competitor"); err == nil {
		_ = competitor.Release()
		t.Fatal("competitor acquired the one core held by the reattached reservation")
	}
	close(gate.release)
	select {
	case got := <-result:
		if got.err != nil || got.outcome != sparkwing.Success {
			t.Fatalf("RunNodeOnce after reattach = %q, %v", got.outcome, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunNodeOnce did not finish after reattach")
	}
	assertOnlyReservationHolder(t, home, reservation.ID())
	if err := reservation.Release(); err != nil {
		t.Fatal(err)
	}
	assertNoReservationHolders(t, home)
	competitor, err := acquireNonblockingCore(home, "competitor-after-release")
	if err != nil {
		t.Fatalf("competitor after release: %v", err)
	}
	if err := competitor.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestWingdExecutorCapacityReservation_LostOwnershipCancelsExecutionBeforeShedding(t *testing.T) {
	registerReservedAdmissionPipeline()
	home := t.TempDir()
	const grace = 500 * time.Millisecond
	first := startE2EDaemonConfig(t, wingd.Config{
		Home: home, Version: "v1", GraceWindow: grace, HeadroomFraction: -1,
		Sampler: e2eSampler{cores: 1},
	})
	ledger := NewWingdExecutorCapacityLedger(home, "v1")
	ledger.spawn = e2eNoSpawn
	summary := reservationSummary(1)
	summary.RunID = "reserved-run"
	summary.NodeID = "work"
	reservation, err := ledger.Reserve(context.Background(), summary, reservationMembership(), executorCapacityLimits{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reservation.Release() })
	admission, err := reservation.Consume()
	if err != nil {
		t.Fatal(err)
	}
	execCtx, cancelExec := reservation.ExecutionContext(context.Background())
	t.Cleanup(cancelExec)
	gate, result := startReservedNodeExecution(t, execCtx, home, admission)
	awaitReservedNodeStart(t, gate)
	if err := first.waitExit(t); err != nil {
		t.Fatalf("first daemon exit: %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("RunNodeOnce transport error: %v", got.err)
		}
		if got.outcome == sparkwing.Success {
			t.Fatal("execution succeeded after reservation ownership was lost")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execution continued after reservation recovery failed")
	}
	if cause := context.Cause(execCtx); cause == nil {
		t.Fatal("execution context has no reservation-loss cause")
	}

	startE2EDaemonConfig(t, wingd.Config{
		Home: home, Version: "v1", GraceWindow: grace, HeadroomFraction: -1,
		Sampler: e2eSampler{cores: 1},
	})
	assertOnlyReservationHolder(t, home, reservation.ID())
	time.Sleep(2 * grace)
	assertNoReservationHolders(t, home)
	if err := reservation.Release(); err != nil {
		t.Fatal(err)
	}
	competitor, err := acquireNonblockingCore(home, "competitor-after-shed")
	if err != nil {
		t.Fatalf("competitor after shed: %v", err)
	}
	if err := competitor.Release(); err != nil {
		t.Fatal(err)
	}
}

func acquireNonblockingCore(home, runID string) (*wingdclient.Lease, error) {
	cl, err := wingdclient.EnsureDaemon(context.Background(), wingdclient.Options{
		Home: home, Version: "v1", Spawn: e2eNoSpawn,
	})
	if err != nil {
		return nil, err
	}
	lease, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID: runID, Resources: wingwire.HostResources{Cores: 1},
		CostSource: wingwire.CostSourcePin, NonBlocking: true,
	}, nil)
	if err != nil {
		_ = cl.Close()
	}
	return lease, err
}

func waitForOnlyReservationHolder(t *testing.T, home, reservationID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "v1"})
		if err == nil && len(state.Holders) == 1 && state.Holders[0].RunID == "executor-reservation/"+reservationID {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reservation %s did not reattach; last state=%+v err=%v", reservationID, state, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertOnlyReservationHolder(t *testing.T, home, reservationID string) {
	t.Helper()
	state, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Waiters) != 0 {
		t.Fatalf("pre-admitted execution attempted a second acquisition: %+v", state.Waiters)
	}
	if len(state.Holders) != 1 || state.Holders[0].RunID != "executor-reservation/"+reservationID {
		t.Fatalf("holders = %+v, want only executor reservation %s", state.Holders, reservationID)
	}
	if state.Holders[0].Resources.Cores != 1 {
		t.Fatalf("reserved cores = %v, want the node's exact 1-core charge", state.Holders[0].Resources.Cores)
	}
}

func assertNoReservationHolders(t *testing.T, home string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, err := wingdclient.Query(context.Background(), wingdclient.Options{Home: home, Version: "v1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Holders) == 0 && len(state.Waiters) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reservation remained after release: holders=%+v waiters=%+v", state.Holders, state.Waiters)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
			if _, err := ledger.Reserve(context.Background(), tc.summary, tc.membership, executorCapacityLimits{}, tc.slot); err == nil {
				t.Fatal("invalid reservation succeeded")
			}
		})
	}
}

func TestWingdExecutorCapacityReservation_ConsumeFailsAfterDaemonLoss(t *testing.T) {
	home := t.TempDir()
	daemon := startE2EDaemonConfig(t, wingd.Config{
		Home: home, Version: "v1", GraceWindow: -1, HeadroomFraction: -1,
		Sampler: e2eSampler{cores: 2},
	})
	ledger := NewWingdExecutorCapacityLedger(home, "v1")
	ledger.spawn = e2eNoSpawn
	reservation, err := ledger.Reserve(context.Background(), reservationSummary(1), reservationMembership(), executorCapacityLimits{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	ownership, stopOwnership := reservation.ExecutionContext(context.Background())
	defer stopOwnership()
	if err := daemon.waitExit(t); err != nil {
		t.Fatalf("daemon exit: %v", err)
	}
	select {
	case <-ownership.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("reservation did not report unrecoverable ownership loss")
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
	if _, err := ledger.Reserve(ctx, reservationSummary(1), reservationMembership(), executorCapacityLimits{}, 0); !errors.Is(err, ErrExecutorCapacityUnavailable) {
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
