package wingd_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func probeHome(ctx context.Context, home string) error {
	socket, err := wingd.EnsureSocketPath(home)
	if err != nil {
		return err
	}
	_, err = client.Probe(ctx, socket)
	return err
}

func startProbeLoop(t *testing.T, interval time.Duration, probe func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			one, cancel := context.WithTimeout(ctx, interval)
			_ = probe(one)
			cancel()
			select {
			case <-ctx.Done():
			case <-time.After(interval):
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			t.Error("idle probe loop did not stop after cancellation")
		}
	})
}

func TestIdleExit_HealthProbeTrafficDoesNotResetIdleClock(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}

	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})

	firstCtx, firstDone := context.WithTimeout(context.Background(), 2*time.Second)
	defer firstDone()
	if err := probeHome(firstCtx, home); err != nil {
		t.Fatalf("health probe against a serving daemon: %v", err)
	}

	startProbeLoop(t, 50*time.Millisecond, func(ctx context.Context) error {
		return probeHome(ctx, home)
	})

	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon with only health-probe traffic should idle out cleanly, got %v", err)
	}
}

type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestIdleExit_QueryTrafficDoesNotResetIdleClock(t *testing.T) {
	t.Parallel()

	const idleTimeout = 300 * time.Millisecond
	const queries = 5
	clock := &stepClock{now: time.Unix(1_700_000_000, 0)}
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: idleTimeout, Now: clock.Now})

	// safety: every query lands with the idle clock short of the timeout, and the final
	// step stays shorter than the timeout, so the daemon can exit only if no query
	// moved its last activity forward.
	step := idleTimeout / (queries + 1)
	for i := range queries {
		clock.advance(step)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := client.Query(ctx, client.Options{Home: home, Version: "test"})
		cancel()
		if err != nil {
			t.Fatalf("query %d against a serving daemon: %v", i+1, err)
		}
	}
	clock.advance(idleTimeout / 2)

	if err := td.waitExit(t, 5*time.Second); err != nil {
		t.Fatalf("daemon with only queue-state query traffic should idle out cleanly, got %v", err)
	}
}

func TestIdleExit_SocketSweepProbeDoesNotResetIdleClock(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}

	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}

	if _, err := client.Probe(context.Background(), sock); err != nil {
		t.Fatalf("probe against a serving daemon: %v", err)
	}

	startProbeLoop(t, 50*time.Millisecond, func(ctx context.Context) error {
		_, err := client.Probe(ctx, sock)
		return err
	})

	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon with only sweep-probe traffic should idle out cleanly, got %v", err)
	}
}

func TestIdleExit_IncompleteConnectionsDoNotResetIdleClock(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}

	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}

	startProbeLoop(t, 50*time.Millisecond, func(ctx context.Context) error {
		nc, err := (&net.Dialer{}).DialContext(ctx, "unix", sock)
		if err == nil {
			_ = nc.Close()
		}
		return err
	})

	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon with only hello-less connections should idle out cleanly, got %v", err)
	}
}
