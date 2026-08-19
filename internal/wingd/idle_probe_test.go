package wingd_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// probeLoop fires probe on an interval until ctx is cancelled, keeping
// continuous probe traffic flowing at a daemon whose idle-exit is under
// test. Probe outcomes are deliberately ignored: once the daemon exits,
// probes fail, and that is the loop's signal to just keep quietly trying
// until the test stops it.
func probeLoop(ctx context.Context, interval time.Duration, probe func(context.Context) error) {
	go func() {
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
}

func TestIdleExit_HealthProbeTrafficDoesNotResetIdleClock(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})

	// One synchronous probe first: the daemon is ready, so this must
	// succeed, proving probes are being served before asserting what
	// they do not do.
	firstCtx, firstDone := context.WithTimeout(context.Background(), 2*time.Second)
	defer firstDone()
	if err := client.HealthProbe(firstCtx, home); err != nil {
		t.Fatalf("health probe against a serving daemon: %v", err)
	}

	ctx, stopProbes := context.WithCancel(context.Background())
	defer stopProbes()
	probeLoop(ctx, 50*time.Millisecond, func(ctx context.Context) error {
		return client.HealthProbe(ctx, home)
	})

	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon with only health-probe traffic should idle out cleanly, got %v", err)
	}
}

func TestIdleExit_QueryTrafficDoesNotResetIdleClock(t *testing.T) {
	const idleTimeout = 300 * time.Millisecond
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: idleTimeout})

	firstCtx, firstDone := context.WithTimeout(context.Background(), 2*time.Second)
	defer firstDone()
	if _, err := client.Query(firstCtx, client.Options{Home: home, Version: "test"}); err != nil {
		t.Fatalf("query against a serving daemon: %v", err)
	}

	ctx, stopQueries := context.WithCancel(context.Background())
	defer stopQueries()
	var successfulQueries, firstSuccess, lastSuccess atomic.Int64
	started := time.Now()
	probeLoop(ctx, 50*time.Millisecond, func(ctx context.Context) error {
		_, err := client.Query(ctx, client.Options{Home: home, Version: "test"})
		if err == nil {
			now := time.Since(started).Nanoseconds()
			firstSuccess.CompareAndSwap(0, now)
			lastSuccess.Store(now)
			successfulQueries.Add(1)
		}
		return err
	})

	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon with only queue-state query traffic should idle out cleanly, got %v", err)
	}
	count := successfulQueries.Load()
	span := time.Duration(lastSuccess.Load() - firstSuccess.Load())
	if count < 3 || span < idleTimeout/2 {
		t.Fatalf("query loop completed %d successful observations over %s; want repeated traffic spanning the idle window", count, span)
	}
}

func TestIdleExit_SocketSweepProbeDoesNotResetIdleClock(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}

	if _, err := client.Probe(context.Background(), sock); err != nil {
		t.Fatalf("probe against a serving daemon: %v", err)
	}

	ctx, stopProbes := context.WithCancel(context.Background())
	defer stopProbes()
	probeLoop(ctx, 50*time.Millisecond, func(ctx context.Context) error {
		_, err := client.Probe(ctx, sock)
		return err
	})

	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon with only sweep-probe traffic should idle out cleanly, got %v", err)
	}
}

func TestIdleExit_PreHelloConnectionsDoNotResetIdleClock(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}

	// Dial and hang up without ever sending a hello, over and over: the
	// shape of a probe whose hello was cut off by its deadline. A peer
	// that never said hello did no work, so its disconnect must not
	// advance the idle clock.
	ctx, stopDialing := context.WithCancel(context.Background())
	defer stopDialing()
	probeLoop(ctx, 50*time.Millisecond, func(context.Context) error {
		nc, err := net.Dial("unix", sock)
		if err == nil {
			_ = nc.Close()
		}
		return err
	})

	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon with only hello-less connections should idle out cleanly, got %v", err)
	}
}

func TestIdleExit_WaitsForWorkingConnections(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 250 * time.Millisecond})

	cl := ensure(t, home, "")
	select {
	case err := <-td.done:
		t.Fatalf("daemon exited while a working client was connected: %v", err)
	case <-time.After(750 * time.Millisecond):
	}

	_ = cl.Close()
	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon should idle out after the working client leaves, got %v", err)
	}
}

// TestIdleExit_GraceThenIdleUnderHealthProbes is the release-host shape: a
// daemon restarted over persisted leases whose owners are gone, with a
// supervisor probing it throughout. The reattach grace must still hold the
// daemon open, and once grace releases the unreclaimed leases, the probes
// must not keep it from idling out.
func TestIdleExit_GraceThenIdleUnderHealthProbes(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
	cl := ensure(t, home, "")
	mustAcquire(t, cl, coreReq("gone-owner", 1))

	// Stop the daemon out from under the holder; the lease persists for
	// a successor, and its owner never comes back.
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
	ctx, stopProbes := context.WithCancel(context.Background())
	defer stopProbes()
	probeLoop(ctx, 50*time.Millisecond, func(ctx context.Context) error {
		return client.HealthProbe(ctx, home)
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
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	nc, err := net.Dial("unix", sock)
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
	_ = nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	if n, err := nc.Read(buf); err == nil {
		t.Fatalf("daemon answered an admission request on a probe connection with %q; want the connection dropped", buf[:n])
	}
}
