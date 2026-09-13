package objectguard_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

func TestCoalescerRunsOnceForOneRequest(t *testing.T) {
	var c objectguard.Coalescer
	done := make(chan struct{})
	var runs atomic.Int64

	if !c.Go(func() { runs.Add(1); close(done) }) {
		t.Fatal("the first request did not start the job")
	}
	<-done
	waitFor(t, "the job to finish", func() bool { return !c.Running() })
	if runs.Load() != 1 {
		t.Errorf("one request ran the job %d times, want 1", runs.Load())
	}
}

func TestCoalescerRerunsForARequestItTurnedAway(t *testing.T) {
	var c objectguard.Coalescer
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var runs atomic.Int64

	if !c.Go(func() {
		runs.Add(1)
		started <- struct{}{}
		<-release
	}) {
		t.Fatal("the first request did not start the job")
	}
	<-started

	for range 3 {
		if c.Go(func() { t.Error("a turned-away request started a second job") }) {
			t.Fatal("a request started a job while one was running")
		}
	}

	close(release)
	waitFor(t, "the repeat to finish", func() bool { return runs.Load() == 2 && !c.Running() })
	if got := runs.Load(); got != 2 {
		t.Errorf("three turned-away requests earned %d runs, want one repeat", got-1)
	}
}

func TestCoalescerRepeatsOnlyOncePerBurst(t *testing.T) {
	var c objectguard.Coalescer
	var runs atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{}, 1)

	c.Go(func() {
		if runs.Add(1) == 1 {
			started <- struct{}{}
			<-release
		}
	})
	<-started
	c.Go(func() {})
	close(release)

	waitFor(t, "the coalescer to settle", func() bool { return !c.Running() })
	if got := runs.Load(); got != 2 {
		t.Errorf("the job ran %d times for one burst, want 2", got)
	}
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
