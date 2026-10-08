package client

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestClientsMissingTheDaemonTogetherStartOne(t *testing.T) {
	home := shortHome(t)
	var spawns atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ready, spawned := make(chan struct{}), make(chan struct{})
	var observed atomic.Int32

	var clients sync.WaitGroup
	for range 8 {
		clients.Add(1)
		go func() {
			defer clients.Done()
			firstFailure := true
			_, _ = EnsureDaemon(ctx, Options{
				Home:    home,
				Version: "v1.0.0",
				Spawn: func(string, string) error {
					if spawns.Add(1) == 1 {
						close(spawned)
					}
					return nil
				},
				observeDialFailure: func() {
					if firstFailure {
						firstFailure = false
						if observed.Add(1) == 8 {
							close(ready)
						}
					}
				},
				DialTimeout: time.Millisecond,
				Backoff:     5 * time.Millisecond,
			})
		}()
	}
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("clients did not attempt their initial connection")
	}
	select {
	case <-spawned:
	case <-ctx.Done():
		t.Fatal("no client claimed daemon start")
	}
	cancel()
	clients.Wait()

	if got := spawns.Load(); got != 1 {
		t.Fatalf("eight clients that found no daemon started %d, want 1: the rest wait for it", got)
	}
}

func TestClientRetriesWhenAnotherStarterReleasesItsClaim(t *testing.T) {
	home := shortHome(t)
	release, claimed, err := wingd.ClaimDaemonStart(home)
	if err != nil || !claimed {
		t.Fatalf("initial startup claim: claimed=%v, err=%v", claimed, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	waiting, spawned, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var failures atomic.Int32
	go func() {
		defer close(done)
		_, _ = EnsureDaemon(ctx, Options{
			Home: home, Version: "v1.0.0", DialTimeout: time.Millisecond,
			observeDialFailure: func() {
				if failures.Add(1) == 2 {
					close(waiting)
				}
			},
			Spawn: func(string, string) error {
				close(spawned)
				return nil
			},
		})
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		release()
		t.Fatal("client did not wait for the existing starter")
	}
	release()
	select {
	case <-spawned:
	case <-ctx.Done():
		t.Fatal("client kept waiting after the startup claim was released")
	}
	cancel()
	<-done
	release, claimed, err = wingd.ClaimDaemonStart(home)
	if err != nil || !claimed {
		t.Fatalf("cancelled client retained startup claim: claimed=%v, err=%v", claimed, err)
	}
	release()
}

func TestCanceledDialFailureDoesNotStartTheDaemon(t *testing.T) {
	home := shortHome(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var spawns atomic.Int32
	_, err := EnsureDaemon(ctx, Options{
		Home: home, Version: "v1.0.0", DialTimeout: time.Millisecond,
		observeDialFailure: cancel,
		Spawn:              func(string, string) error { spawns.Add(1); return nil },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want cancellation", err)
	}
	if got := spawns.Load(); got != 0 {
		t.Errorf("canceled caller started %d daemons", got)
	}
	release, claimed, err := wingd.ClaimDaemonStart(home)
	if err != nil || !claimed {
		t.Fatalf("canceled caller retained startup claim: claimed=%v error=%v", claimed, err)
	}
	release()
}

func TestDaemonUnreachableRetainsCancellationAfterDialTimeout(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Net: "unix", Err: context.DeadlineExceeded}
	err := daemonUnreachable(shortHome(t), "fixture.sock", 0, context.Canceled, dialErr)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrDaemonUnreachable) || !errors.Is(err, dialErr) {
		t.Fatalf("dial timeout lost cancellation or reachability evidence: %v", err)
	}
}
