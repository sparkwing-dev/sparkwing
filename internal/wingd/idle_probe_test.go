package wingd_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
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

func TestIdleExit_PreHelloConnectionsDoNotResetIdleClock(t *testing.T) {
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

func TestIdleExit_GraceThenIdleUnderHealthProbes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.8s of real work; the fast class runs under -short")
	}

	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
	cl := ensure(t, home, "")
	mustAcquire(t, cl, coreReq("gone-owner", 1))

	td.stop()
	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("first daemon stop: %v", err)
	}
	_ = cl.Close()

	td2 := startDaemon(t, wingd.Config{
		Home:        home,
		IdleTimeout: 300 * time.Millisecond,
		GraceWindow: 500 * time.Millisecond,
	})
	startProbeLoop(t, 50*time.Millisecond, func(ctx context.Context) error {
		return probeHome(ctx, home)
	})

	select {
	case err := <-td2.done:
		t.Fatalf("daemon exited inside the reattach grace window: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	if err := td2.waitExit(t, 5*time.Second); err != nil {
		t.Fatalf("daemon should idle out after grace expires despite probes, got %v", err)
	}
}

func TestHealthProbeConnectionMayOnlyReadQueueState(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home})
	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	nc, err := wingd.DialSocket(context.Background(), sock, 3*time.Second)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	t.Cleanup(func() { _ = nc.Close() })

	if err := writeRawMessage(nc, &wingwire.Hello{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "test", HealthProbe: true}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	if _, ok := readRawMessage(t, nc).(*wingwire.HelloAck); !ok {
		t.Fatal("probe hello not acknowledged")
	}
	if err := writeRawMessage(nc, &wingwire.QueueState{}); err != nil {
		t.Fatalf("write queue-state: %v", err)
	}
	if _, ok := readRawMessage(t, nc).(*wingwire.QueueState); !ok {
		t.Fatal("probe connection could not read queue state")
	}

	if err := writeRawMessage(nc, &wingwire.AdmissionRequest{RunID: "sneak", Resources: wingwire.HostResources{Cores: 1}}); err != nil {
		t.Fatalf("write admission request: %v", err)
	}
	msg := readRawMessage(t, nc)
	unsupported, ok := msg.(*wingwire.Unsupported)
	if !ok {
		t.Fatalf("daemon answered an admission request on a probe connection with %T; want it refused", msg)
	}
	if unsupported.Type != string(wingwire.TypeAdmissionRequest) {
		t.Errorf("refusal names %q, want %q", unsupported.Type, wingwire.TypeAdmissionRequest)
	}
}
