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
	g, releaseDescendants := parkCleanupOnStubbornDescendants(t)

	killed := make(chan error, 1)
	go func() { killed <- g.Kill() }()
	select {
	case err := <-killed:
		if err != nil {
			t.Fatalf("kill during cleanup: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("kill blocked behind the descendant wait it exists to shortcut")
	}

	releaseDescendants()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !g.Reaped() {
		select {
		case <-deadline.C:
			t.Fatalf("group %d was not reaped after the descendants cleared", g.ID())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestTerminateLeavesOnItsOwnDeadlineWhileCleanupIsParked(t *testing.T) {
	g, _ := parkCleanupOnStubbornDescendants(t)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	terminated := make(chan error, 1)
	start := time.Now()
	go func() { terminated <- g.Terminate(ctx, 10*time.Millisecond) }()
	select {
	case err := <-terminated:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("terminate during a parked cleanup = %v, want its own deadline", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("terminate took %s to honour a 500ms deadline", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminate blocked past its deadline behind a parked cleanup")
	}
}

func parkCleanupOnStubbornDescendants(t *testing.T) (*Group, func()) {
	t.Helper()
	g := startHelper(t, "short")
	t.Cleanup(func() {
		if !g.Reaped() {
			terminateForTest(g)
		}
	})
	select {
	case <-g.LeaderExited():
	case <-time.After(3 * time.Second):
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
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("timed out joining the cleanup that held the descendant wait")
		}
	})

	select {
	case <-waiting:
	case err := <-finished:
		t.Fatalf("cleanup returned before it reached the descendant wait: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup never reached the descendant wait")
	}
	return g, releaseDescendants
}
