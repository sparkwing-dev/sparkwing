package client

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestClientsMissingTheDaemonTogetherStartOne(t *testing.T) {
	home := shortHome(t)
	var spawns atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var clients sync.WaitGroup
	for range 8 {
		clients.Add(1)
		go func() {
			defer clients.Done()
			_, _ = EnsureDaemon(ctx, Options{
				Home:        home,
				Version:     "v1.0.0",
				Spawn:       func(string, string) error { spawns.Add(1); return nil },
				DialTimeout: time.Millisecond,
				Backoff:     5 * time.Millisecond,
			})
		}()
	}
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
