//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKillAnswersWhileCleanupWaitsOnStubbornDescendants(t *testing.T) {
	g, releaseDescendants, finished := parkCleanupOnStubbornDescendants(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	killed := make(chan error, 1)
	go func() { killed <- g.Kill() }()
	select {
	case err := <-killed:
		if err != nil {
			t.Fatalf("kill during cleanup: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("kill blocked behind the descendant wait it exists to shortcut")
	}

	releaseDescendants()
	joined, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("finish after descendants cleared: %v", err)
		}
	case <-joined.Done():
		t.Fatalf("group %d did not finish after the descendants cleared", g.ID())
	}
	if !g.Reaped() {
		t.Fatalf("group %d finished without reaping its leader", g.ID())
	}
}

func TestTerminateLeavesOnItsOwnDeadlineWhileCleanupIsParked(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.6s of real work; the fast class runs under -short")
	}
	g, _, _ := parkCleanupOnStubbornDescendants(t)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	terminated := make(chan error, 1)
	go func() { terminated <- g.Terminate(ctx, 10*time.Millisecond) }()
	joined, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	select {
	case err := <-terminated:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("terminate during a parked cleanup = %v, want its own deadline", err)
		}
	case <-joined.Done():
		t.Fatal("terminate blocked past its deadline behind a parked cleanup")
	}
}

func parkCleanupOnStubbornDescendants(t *testing.T) (*Group, func(), <-chan error) {
	t.Helper()
	g := startHelper(t, "short")
	t.Cleanup(func() {
		if !g.Reaped() {
			terminateForTest(g)
		}
	})
	ready, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	select {
	case <-g.LeaderExited():
	case <-ready.Done():
		t.Fatal("leader did not exit")
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseDescendants := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseDescendants)

	waiting := make(chan struct{})
	var waitingOnce sync.Once
	probes := &atomic.Int64{}
	g.SetDescendantProbe(func(int, bool, bool) (bool, error) {
		select {
		case <-release:
			return true, nil
		default:
		}
		if probes.Add(1) >= 4 {
			waitingOnce.Do(func() { close(waiting) })
		}
		return false, nil
	})

	finished := make(chan error, 1)
	go func() {
		finished <- g.Finish(context.Background(), time.Millisecond)
		close(finished)
	}()
	t.Cleanup(func() {
		releaseDescendants()
		joined, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		select {
		case <-finished:
		case <-joined.Done():
			t.Error("timed out joining the cleanup that held the descendant wait")
		}
	})

	parked, stopParked := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopParked()
	select {
	case <-waiting:
	case err := <-finished:
		t.Fatalf("cleanup returned before it reached the descendant wait: %v", err)
	case <-parked.Done():
		t.Fatal("cleanup never reached the descendant wait")
	}
	return g, releaseDescendants, finished
}
