package wingd_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func coreReq(runID string, cores float64) wingwire.AdmissionRequest {
	return wingwire.AdmissionRequest{
		RunID:     runID,
		Resources: wingwire.HostResources{Cores: cores},
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
	deadline := time.After(3 * time.Second)
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
			case <-deadline:
				t.Fatal("no daemon won the election")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	lost := 0
	loseDeadline := time.After(3 * time.Second)
	for lost < n-1 {
		select {
		case err := <-errs:
			if !errors.Is(err, wingd.ErrNotElected) {
				t.Fatalf("loser returned %v, want ErrNotElected", err)
			}
			lost++
		case <-loseDeadline:
			t.Fatalf("only %d of %d losers reported ErrNotElected", lost, n-1)
		}
	}
	if winners != 1 {
		t.Fatalf("saw %d ready daemons, want exactly 1", winners)
	}
	cancel()
	wg.Wait()
}

func TestHolderDisconnect_ReleasesAndPromotes(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	holder := mustAcquire(t, a, semReq("a", "deploy", 1, 1, wingwire.PolicyQueue))
	_ = holder

	b := ensure(t, home, "")
	positions, resultB := acquireAsync(b, semReq("b", "deploy", 1, 1, wingwire.PolicyQueue))
	select {
	case q := <-positions:
		if q.Position < 1 {
			t.Fatalf("b queued at position %d, want >=1", q.Position)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b never reported a queue position")
	}

	a.Close()

	r := waitResult(t, resultB, 2*time.Second)
	if r.err != nil {
		t.Fatalf("b should have been promoted, got %v", r.err)
	}
	if r.lease.RunID != "b" {
		t.Fatalf("promoted lease run id %q, want b", r.lease.RunID)
	}
}

func TestExplicitRelease_Promotes(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})

	a := ensure(t, home, "")
	holder := mustAcquire(t, a, semReq("a", "lock", 1, 1, wingwire.PolicyQueue))

	b := ensure(t, home, "")
	_, resultB := acquireAsync(b, semReq("b", "lock", 1, 1, wingwire.PolicyQueue))
	time.Sleep(100 * time.Millisecond)

	if err := holder.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	r := waitResult(t, resultB, 2*time.Second)
	if r.err != nil {
		t.Fatalf("b should have been promoted after release, got %v", r.err)
	}
}

// TestWaiterDisconnect_UnblocksLighterWaiter kills the head-of-line
// waiter and asserts the lighter waiter it was blocking is promoted --
// the case the ledger's snapshot-rebuild cancellation must get right.
func TestWaiterDisconnect_UnblocksLighterWaiter(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
	})

	h := ensure(t, home, "")
	mustAcquire(t, h, coreReq("h", 3))

	w1 := ensure(t, home, "")
	pos1, _ := acquireAsync(w1, coreReq("w1", 2))
	waitForQueue(t, pos1)

	w2 := ensure(t, home, "")
	pos2, result2 := acquireAsync(w2, coreReq("w2", 1))
	waitForQueue(t, pos2)

	w1.Close()

	r := waitResult(t, result2, 2*time.Second)
	if r.err != nil {
		t.Fatalf("w2 should promote once w1 leaves, got %v", r.err)
	}
	if r.lease.RunID != "w2" {
		t.Fatalf("promoted %q, want w2", r.lease.RunID)
	}
}

func TestChildAttach_SharesLeaseWithoutDoubleCharge(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{
		Home:             home,
		Sampler:          newFakeSampler(4, 8<<30),
		HeadroomFraction: -1,
	})

	parent := ensure(t, home, "")
	pl := mustAcquire(t, parent, coreReq("p", 2))

	child := ensure(t, home, "")
	cl, err := child.Acquire(context.Background(), wingwire.AdmissionRequest{
		RunID:            "c",
		ParentLeaseToken: pl.Token,
	}, nil)
	if err != nil {
		t.Fatalf("child attach: %v", err)
	}
	if cl.Token != pl.Token {
		t.Fatalf("child token %q, want parent token %q", cl.Token, pl.Token)
	}

	q := ensure(t, home, "")
	qs, err := q.QueueState(context.Background())
	if err != nil {
		t.Fatalf("queue state: %v", err)
	}
	held := resourceHeld(qs, "cores")
	if held != 2 {
		t.Fatalf("cores held %v, want 2 (child must not double-charge)", held)
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
