//go:build !windows

package procgroup

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKillAnswersWhileCleanupWaitsOnStubbornDescendants(t *testing.T) {
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
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("cleanup after the descendants cleared: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not complete after the descendants cleared")
	}
	if !g.Reaped() {
		t.Fatalf("group %d was not reaped", g.ID())
	}
}
