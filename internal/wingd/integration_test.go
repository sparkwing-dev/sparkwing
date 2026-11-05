package wingd_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func coreReq(runID string, cores float64) wingwire.AdmissionRequest {
	return wingwire.AdmissionRequest{
		RunID:     wingwire.HolderRef(runID),
		Resources: wingwire.HostResources{Cores: cores},
	}
}

func TestAdmissionDefaultsToClassic(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	state, err := client.Query(context.Background(), client.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if state.AdmissionMode != "classic" {
		t.Fatalf("admission mode = %q, want classic", state.AdmissionMode)
	}
}

func TestAdmissionModeOffDoesNotGateHostCapacity(t *testing.T) {
	home := shortHome(t)
	policy := wingd.AdmissionPolicy{Mode: admission.ModeOff, Scheduling: admission.AutoPolicy()}
	startDaemon(t, wingd.Config{
		Home: home, Sampler: newFakeSampler(4, 8<<30), HeadroomFraction: -1, AdmissionPolicy: &policy,
	})
	first := ensure(t, home, "")
	defer first.Close()
	second := ensure(t, home, "")
	defer second.Close()
	mustAcquire(t, first, coreReq("first", 4))
	mustAcquire(t, second, coreReq("second", 4))
	state, err := client.Query(context.Background(), client.Options{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if state.AdmissionMode != "off" || len(state.Holders) != 2 || len(state.Waiters) != 0 {
		t.Fatalf("queue state = %+v", state)
	}
}

func TestElection_ExactlyOneWinner(t *testing.T) {
	home := shortHome(t)
	const n = 8
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemons := make([]*wingd.Daemon, n)
	errs := make(chan error, n)
	for i := range daemons {
		d, err := wingd.New(wingd.Config{Home: home, Sampler: newFakeSampler(8, 8<<30)})
		if err != nil {
			t.Fatalf("new daemon %d: %v", i, err)
		}
		daemons[i] = d
	}
	var wg sync.WaitGroup
	for _, d := range daemons {
		wg.Add(1)
		go func(d *wingd.Daemon) {
			defer wg.Done()
			errs <- d.Run(ctx)
		}(d)
	}

	var winners int
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for winners == 0 {
		for _, d := range daemons {
			select {
			case <-d.Ready():
				winners++
			default:
			}
		}
		if winners == 0 {
			select {
			case <-deadline.C:
				t.Fatal("no daemon won the election")
			case <-poll.C:
			}
		}
	}
	poll.Stop()

	lost := 0
	loseDeadline := time.NewTimer(3 * time.Second)
	defer loseDeadline.Stop()
	for lost < n-1 {
		select {
		case err := <-errs:
			if !errors.Is(err, wingd.ErrNotElected) {
				t.Fatalf("loser returned %v, want ErrNotElected", err)
			}
			lost++
		case <-loseDeadline.C:
			t.Fatalf("only %d of %d losers reported ErrNotElected", lost, n-1)
		}
	}
	if winners != 1 {
		t.Fatalf("saw %d ready daemons, want exactly 1", winners)
	}
	cancel()
	wg.Wait()
}

func TestPromotionRebroadcastsRemainingWaiterPosition(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	holder := mustAcquire(t, a, semReq("a", "deploy", 1, 1, wingwire.PolicyQueue))

	b := ensure(t, home, "")
	positionsB, resultB := acquireAsync(t, b, semReq("b", "deploy", 1, 1, wingwire.PolicyQueue))
	select {
	case q := <-positionsB:
		if q.Position != 1 {
			t.Fatalf("b initial position = %d, want 1", q.Position)
		}
	case r := <-resultB:
		t.Fatalf("b resolved before queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("b never reported its initial queue position")
	}

	c := ensure(t, home, "")
	positionsC, resultC := acquireAsync(t, c, semReq("c", "deploy", 1, 1, wingwire.PolicyQueue))
	select {
	case q := <-positionsC:
		if q.Position != 2 {
			t.Fatalf("c initial position = %d, want 2", q.Position)
		}
	case r := <-resultC:
		t.Fatalf("c resolved before queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("c never reported its initial queue position")
	}

	if err := holder.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	r := waitResult(t, resultB, 2*time.Second)
	if r.err != nil {
		t.Fatalf("b should have promoted, got %v", r.err)
	}

	select {
	case q := <-positionsC:
		if q.Position != 1 {
			t.Fatalf("c refreshed position = %d, want 1", q.Position)
		}
	case r := <-resultC:
		t.Fatalf("c resolved instead of remaining queued: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("c never received refreshed queue position after b promoted")
	}
}

func TestPositionRebroadcastKeepsIndependentQueuesSeparate(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	semOnly := func(runID, key string) wingwire.AdmissionRequest {
		return wingwire.AdmissionRequest{
			RunID:          wingwire.HolderRef(runID),
			SemaphoresOnly: true,
			Semaphores: []wingwire.SemaphoreClaim{
				{Name: key, Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue},
			},
		}
	}

	a := ensure(t, home, "")
	holderA := mustAcquire(t, a, semOnly("a", "deploy-a"))
	b := ensure(t, home, "")
	mustAcquire(t, b, semOnly("b", "deploy-b"))
	c := ensure(t, home, "")
	mustAcquire(t, c, semOnly("c", "deploy-c"))

	waitA := ensure(t, home, "")
	_, resultWaitA := acquireAsync(t, waitA, semOnly("wait-a", "deploy-a"))
	waitB := ensure(t, home, "")
	waitBPositions, waitBResult := acquireAsync(t, waitB, semOnly("wait-b", "deploy-b"))
	waitC := ensure(t, home, "")
	waitCPositions, waitCResult := acquireAsync(t, waitC, semOnly("wait-c", "deploy-c"))

	select {
	case q := <-waitBPositions:
		if q.Position != 1 {
			t.Fatalf("wait-b initial position = %d, want 1", q.Position)
		}
	case r := <-waitBResult:
		t.Fatalf("wait-b resolved before queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("wait-b never reported its initial queue position")
	}
	select {
	case q := <-waitCPositions:
		if q.Position != 1 {
			t.Fatalf("wait-c initial position = %d, want 1", q.Position)
		}
	case r := <-waitCResult:
		t.Fatalf("wait-c resolved before queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("wait-c never reported its initial queue position")
	}

	if err := holderA.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	r := waitResult(t, resultWaitA, 2*time.Second)
	if r.err != nil {
		t.Fatalf("wait-a should have promoted, got %v", r.err)
	}

	select {
	case q := <-waitBPositions:
		if q.Position != 1 {
			t.Fatalf("wait-b refreshed position = %d, want 1", q.Position)
		}
	case r := <-waitBResult:
		t.Fatalf("wait-b resolved instead of remaining queued: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("wait-b never received refreshed queue position")
	}
	select {
	case q := <-waitCPositions:
		if q.Position != 1 {
			t.Fatalf("wait-c refreshed position = %d, want 1", q.Position)
		}
	case r := <-waitCResult:
		t.Fatalf("wait-c resolved instead of remaining queued: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("wait-c never received refreshed queue position")
	}
}

func TestQueuedHostPressureDoesNotReportColdSemaphoreKey(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(1, 8<<30),
		HeadroomFraction: -1,
	})

	holderClient := ensure(t, home, "")
	holder := mustAcquire(t, holderClient, coreReq("host-holder", 1))
	defer func() { _ = holder.Release() }()

	waiterReq := semReq("host-waiter", "cold-lock", 1, 1, wingwire.PolicyQueue)
	waiterReq.Resources = wingwire.HostResources{Cores: 1}
	waiter := ensure(t, home, "")
	positions, _ := acquireAsync(t, waiter, waiterReq)
	q := <-positions

	if q.Key != "" {
		t.Fatalf("queued key = %q, want empty while only host capacity blocks", q.Key)
	}
}

func TestMeasuredRequestAboveIdleGrantableCapacityIsAdmitted(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	cl := ensure(t, home, "")
	lease := mustAcquire(t, cl, wingwire.AdmissionRequest{
		RunID:      "measured-heavy",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 10},
	})
	if lease.Resources.Cores != 6.4 {
		t.Fatalf("admitted cores = %v, want idle grantable ceiling 6.4", lease.Resources.Cores)
	}
}

func TestOversizedMeasuredCPURequestQueuesFollower(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30), HeadroomFraction: -1})

	holderClient := ensure(t, home, "")
	lease := mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID:      "oversized",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 10},
	})
	if lease.Resources.Cores != 8 {
		t.Fatalf("oversized charge = %v, want full host charge 8", lease.Resources.Cores)
	}

	followerClient := ensure(t, home, "")
	positions, result := acquireAsync(t, followerClient, wingwire.AdmissionRequest{
		RunID:      "follower",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 1},
	})
	select {
	case <-positions:
	case r := <-result:
		t.Fatalf("follower resolved without queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("follower neither queued nor resolved")
	}
}

func TestLivenessFloor_AdmitsSoleRunUnderExternalLoad(t *testing.T) {
	home := shortHome(t)
	sampler := newFakeSampler(8, 16<<30)
	sampler.set(wingd.HostStat{TotalCores: 8, TotalMemoryBytes: 16 << 30, FreeMemoryBytes: 16 << 30, LoadAverage: 100, BusyCores: 100, LoadMeasured: true, CPUMeasured: true, MemoryMeasured: true})
	startDaemon(t, wingd.Config{Home: home, Sampler: sampler})

	cl := ensure(t, home, "")
	lease := mustAcquire(t, cl, wingwire.AdmissionRequest{
		RunID:      "sole",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 10},
	})
	if !lease.SoleRunUnderLoad {
		t.Fatal("sole run under external load was not flagged SoleRunUnderLoad")
	}
	if lease.Resources.Cores != 6.4 {
		t.Fatalf("sole run charge = %v, want grantable 6.4", lease.Resources.Cores)
	}

	second := ensure(t, home, "")
	positions, result := acquireAsync(t, second, wingwire.AdmissionRequest{
		RunID:      "second",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 1},
	})
	select {
	case <-positions:
	case r := <-result:
		t.Fatalf("second run resolved without queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("second run neither queued nor resolved")
	}
}

func TestLivenessFloor_ZeroCostConnectionsDoNotSuppressFIFOHead(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	home := shortHome(t)
	sampler := newFakeSampler(8, 16<<30)
	sampler.set(wingd.HostStat{
		TotalCores:       8,
		TotalMemoryBytes: 16 << 30,
		FreeMemoryBytes:  1 << 30,
		LoadAverage:      100,
		LoadMeasured:     true,
		MemoryMeasured:   true,
	})
	startDaemon(t, wingd.Config{Home: home, Sampler: sampler})

	for i := 1; i <= 3; i++ {
		registration := ensure(t, home, "")
		mustAcquire(t, registration, wingwire.AdmissionRequest{
			RunID:          wingwire.HolderRef(fmt.Sprintf("run-%d", i)),
			SemaphoresOnly: true,
		})
	}

	blockerClient := ensure(t, home, "")
	blocker := mustAcquire(t, blockerClient, wingwire.AdmissionRequest{
		RunID:      "real-holder",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 1, MemoryBytes: 1 << 30},
	})

	results := make([]<-chan acquireResult, 3)
	for i := range results {
		cl := ensure(t, home, "")
		positions, result := acquireAsync(t, cl, wingwire.AdmissionRequest{
			RunID:        wingwire.HolderRef(fmt.Sprintf("run-%d/node-host/bm9kZQ", i+1)),
			OwnerRunID:   fmt.Sprintf("run-%d", i+1),
			DisplayRunID: fmt.Sprintf("run-%d/node", i+1),
			SubLease:     true,
			CostSource:   wingwire.CostSourceMeasured,
			Resources:    wingwire.HostResources{Cores: 4, MemoryBytes: 4 << 30},
		})
		select {
		case q := <-positions:
			if q.Position != i+1 {
				t.Fatalf("node %d position = %d, want FIFO position %d", i+1, q.Position, i+1)
			}
		case r := <-result:
			t.Fatalf("node %d resolved before blocker release: lease=%v err=%v", i+1, r.lease, r.err)
		case <-time.After(2 * time.Second):
			t.Fatalf("node %d neither queued nor resolved", i+1)
		}
		results[i] = result
	}

	if err := blocker.Release(); err != nil {
		t.Fatalf("release blocker: %v", err)
	}
	first := waitResult(t, results[0], 2*time.Second)
	if first.err != nil || first.lease == nil {
		t.Fatalf("FIFO head did not bootstrap: lease=%v err=%v", first.lease, first.err)
	}
	for i := 1; i < len(results); i++ {
		select {
		case r := <-results[i]:
			t.Fatalf("node %d also resolved while head holds resources: lease=%v err=%v", i+1, r.lease, r.err)
		case <-time.After(100 * time.Millisecond):
		}
	}

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	connected, holding := 0, 0
	for _, h := range qs.Holders {
		if h.ConnectionOnly {
			connected++
		} else {
			holding++
		}
	}
	if connected != 3 || holding != 1 || len(qs.Waiters) != 2 {
		t.Fatalf("queue after bootstrap = %d connected, %d holding, %d waiting; want 3, 1, 2", connected, holding, len(qs.Waiters))
	}
}

func TestServiceOrderPromotesEarlierParticipantAcrossOwners(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30)})

	olderOwnerClient := ensure(t, home, "")
	olderOwner := mustAcquire(t, olderOwnerClient, wingwire.AdmissionRequest{
		RunID: "owner-older", SemaphoresOnly: true,
	})
	newerOwnerClient := ensure(t, home, "")
	newerOwner := mustAcquire(t, newerOwnerClient, wingwire.AdmissionRequest{
		RunID: "owner-newer", SemaphoresOnly: true,
	})
	blockerClient := ensure(t, home, "")
	blocker := mustAcquire(t, blockerClient, wingwire.AdmissionRequest{
		RunID: "blocker", Resources: wingwire.HostResources{Cores: 4},
	})

	newerChildClient := ensure(t, home, "")
	newerPositions, newerResult := acquireAsync(t, newerChildClient, wingwire.AdmissionRequest{
		RunID: "newer-child", OwnerRunID: "owner-newer", OwnerLeaseToken: newerOwner.Token, SubLease: true,
		Resources: wingwire.HostResources{Cores: 4},
	})
	select {
	case <-newerPositions:
	case <-time.After(2 * time.Second):
		t.Fatal("newer child did not queue")
	}

	olderChildClient := ensure(t, home, "")
	olderPositions, olderResult := acquireAsync(t, olderChildClient, wingwire.AdmissionRequest{
		RunID: "older-child", OwnerRunID: "owner-older", OwnerLeaseToken: olderOwner.Token, SubLease: true,
		Resources: wingwire.HostResources{Cores: 4},
	})
	select {
	case q := <-olderPositions:
		if q.Position != 2 {
			t.Fatalf("older child position = %d, want 2 behind the earlier participant", q.Position)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("older child did not queue")
	}

	if err := blocker.Release(); err != nil {
		t.Fatalf("release blocker: %v", err)
	}
	first := waitResult(t, newerResult, 2*time.Second)
	if first.err != nil || first.lease == nil {
		t.Fatalf("earlier participant was not promoted: lease=%v err=%v", first.lease, first.err)
	}
	select {
	case r := <-olderResult:
		t.Fatalf("later participant resolved first: lease=%v err=%v", r.lease, r.err)
	case <-time.After(100 * time.Millisecond):
	}

	_ = olderOwner.Release()
	_ = newerOwner.Release()
}

func TestOwnerRunAdmissionAuthorityRejectsRankTheft(t *testing.T) {
	tests := []struct {
		name       string
		ownerSetup func(t *testing.T, home string) (claimedOwner, proofToken string)
	}{
		{
			name: "wrong top-level owner",
			ownerSetup: func(t *testing.T, home string) (string, string) {
				claimed := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "claimed-owner", SemaphoresOnly: true,
				})
				actual := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "actual-owner", SemaphoresOnly: true,
				})
				t.Cleanup(func() { _ = claimed.Release(); _ = actual.Release() })
				return "claimed-owner", actual.Token
			},
		},
		{
			name: "non-top-level participant",
			ownerSetup: func(t *testing.T, home string) (string, string) {
				owner := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "top-owner", SemaphoresOnly: true,
				})
				participant := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "internal-participant", OwnerRunID: "top-owner", OwnerLeaseToken: owner.Token,
					SemaphoresOnly: true, SubLease: true,
				})
				t.Cleanup(func() { _ = participant.Release(); _ = owner.Release() })
				return "internal-participant", participant.Token
			},
		},
		{
			name: "non-live owner",
			ownerSetup: func(t *testing.T, home string) (string, string) {
				owner := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
					RunID: "released-owner", SemaphoresOnly: true,
				})
				token := owner.Token
				if err := owner.Release(); err != nil {
					t.Fatalf("release owner: %v", err)
				}
				return "released-owner", token
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := shortHome(t)
			startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30)})
			claimedOwner, proofToken := tt.ownerSetup(t, home)
			blocker := mustAcquire(t, ensure(t, home, ""), wingwire.AdmissionRequest{
				RunID: "blocker", Resources: wingwire.HostResources{Cores: 4},
			})
			t.Cleanup(func() { _ = blocker.Release() })

			baselinePositions, _ := acquireAsync(t, ensure(t, home, ""), wingwire.AdmissionRequest{
				RunID: "baseline", Resources: wingwire.HostResources{Cores: 4},
			})
			select {
			case <-baselinePositions:
			case <-time.After(2 * time.Second):
				t.Fatal("baseline did not queue")
			}
			forgedPositions, _ := acquireAsync(t, ensure(t, home, ""), wingwire.AdmissionRequest{
				RunID: "forged", OwnerRunID: claimedOwner, OwnerLeaseToken: proofToken, SubLease: true,
				Resources: wingwire.HostResources{Cores: 4},
			})
			select {
			case q := <-forgedPositions:
				if q.Position != 2 {
					t.Fatalf("forged position = %d, want 2 behind earlier fallback participant", q.Position)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("forged request did not queue")
			}
		})
	}
}

func TestMeasuredCPUDeficitAdmitsOneAdditionalMemoryFittingRun(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30), HeadroomFraction: -1})

	holderClient := ensure(t, home, "")
	mustAcquire(t, holderClient, wingwire.AdmissionRequest{
		RunID:      "holder",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 6, MemoryBytes: 2 << 30},
	})

	headClient := ensure(t, home, "")
	lease := mustAcquire(t, headClient, wingwire.AdmissionRequest{
		RunID:      "head",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 6, MemoryBytes: 2 << 30},
	})
	if lease.Resources.Cores != 6 {
		t.Fatalf("head cores = %v, want measured charge retained", lease.Resources.Cores)
	}

	nextClient := ensure(t, home, "")
	positions, result := acquireAsync(t, nextClient, wingwire.AdmissionRequest{
		RunID:      "next",
		CostSource: wingwire.CostSourceMeasured,
		Resources:  wingwire.HostResources{Cores: 1, MemoryBytes: 2 << 30},
	})
	select {
	case <-positions:
	case r := <-result:
		t.Fatalf("next run resolved without queueing: lease=%v err=%v", r.lease, r.err)
	case <-time.After(2 * time.Second):
		t.Fatal("next run neither queued nor resolved")
	}
}

func TestPinnedRequestAboveCapacityFails(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	cl := ensure(t, home, "")
	_, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:      "pinned-heavy",
		CostSource: wingwire.CostSourcePin,
		Resources:  wingwire.HostResources{Cores: 10},
	}, nil)
	if err == nil {
		t.Fatal("oversized pin admitted, want never-admissible failure")
	}
}

func TestUnknownCostSourceFails(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	cl := ensure(t, home, "")
	_, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:      "unknown-source",
		CostSource: wingwire.CostSource("typo"),
		Resources:  wingwire.HostResources{Cores: 1},
	}, nil)
	if err == nil {
		t.Fatal("unknown cost source admitted, want invalid request failure")
	}
}

func TestAutoMeasuredCostSourcesAdmitOnIdleBox(t *testing.T) {
	for _, source := range []wingwire.CostSource{wingwire.CostSourceMeasuring, wingwire.CostSourceFloor} {
		t.Run(string(source), func(t *testing.T) {
			home := shortHome(t)
			startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

			cl := ensure(t, home, "")
			lease, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
				RunID:      "auto-measured",
				CostSource: source,
				Resources:  wingwire.HostResources{Cores: 1.5},
			}, nil)
			if err != nil {
				t.Fatalf("auto-measured %s request rejected on an idle box: %v", source, err)
			}
			if lease == nil {
				t.Fatalf("auto-measured %s request returned no lease", source)
			}
		})
	}
}

func TestUnknownCostSourceNamesTheOffendingInput(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	cl := ensure(t, home, "")
	_, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:      "unknown-source",
		CostSource: wingwire.CostSource("typo"),
		Resources:  wingwire.HostResources{Cores: 1},
	}, nil)
	if err == nil {
		t.Fatal("unknown cost source admitted, want invalid request failure")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("rejection %q does not name the offending cost source", err)
	}
}

func TestRepeatedInvalidRequestsSurfaceInQueueStateWindow(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(8, 16<<30)})

	for i := 0; i < 3; i++ {
		cl := ensure(t, home, "")
		_, err := cl.Acquire(context.Background(), wingwire.AdmissionRequest{
			RunID:      wingwire.HolderRef("bad-" + strconv.Itoa(i)),
			CostSource: wingwire.CostSource("typo"),
			Resources:  wingwire.HostResources{Cores: 1},
		}, nil)
		if err == nil {
			t.Fatal("invalid request admitted")
		}
	}

	qs, err := client.Query(context.Background(), client.Options{Home: home, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if qs.Events == nil {
		t.Fatal("events window nil after rejections")
	}
	var count int
	for _, r := range qs.Events.Rejections {
		if r.Cause == "cost_source" {
			count = r.Count
		}
	}
	if count != 3 {
		t.Fatalf("cost_source rejection count = %d, want 3 (window: %+v)", count, qs.Events.Rejections)
	}
}

func TestExplicitRelease_Promotes(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	holder := mustAcquire(t, a, semReq("a", "lock", 1, 1, wingwire.PolicyQueue))

	b := ensure(t, home, "")
	positions, resultB := acquireAsync(t, b, semReq("b", "lock", 1, 1, wingwire.PolicyQueue))
	waitForQueue(t, positions)
	started := time.Now()

	if err := holder.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	r := waitResult(t, resultB, 2*time.Second)
	if r.err != nil {
		t.Fatalf("b should have been promoted after release, got %v", r.err)
	}
	if elapsed := time.Since(started); elapsed >= 90*time.Millisecond {
		t.Fatalf("release-to-promotion took %v, want less than 90ms", elapsed)
	}
}

func waitForQueue(t *testing.T, positions <-chan wingwire.Queued) {
	t.Helper()
	select {
	case <-positions:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never reported a queue position")
	}
}

func resourceHeld(qs wingwire.QueueState, key string) float64 {
	for _, r := range qs.Resources {
		if r.Key == key {
			return r.Held
		}
	}
	return -1
}
