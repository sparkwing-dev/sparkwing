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

	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})

	firstCtx, firstDone := context.WithTimeout(context.Background(), 2*time.Second)
	defer firstDone()
	if err := client.HealthProbe(firstCtx, home); err != nil {
		t.Fatalf("health probe against a serving daemon: %v", err)
	}

	startProbeLoop(t, 50*time.Millisecond, func(ctx context.Context) error {
		return client.HealthProbe(ctx, home)
	})

	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon with only health-probe traffic should idle out cleanly, got %v", err)
	}
}

func TestIdleExit_QueryTrafficDoesNotResetIdleClock(t *testing.T) {
	t.Parallel()

	const idleTimeout = 300 * time.Millisecond
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: idleTimeout})

	firstCtx, firstDone := context.WithTimeout(context.Background(), 2*time.Second)
	defer firstDone()
	if _, err := client.Query(firstCtx, client.Options{Home: home, Version: "test"}); err != nil {
		t.Fatalf("query against a serving daemon: %v", err)
	}

	var successfulQueries, firstSuccess, lastSuccess atomic.Int64
	started := time.Now()
	startProbeLoop(t, 50*time.Millisecond, func(ctx context.Context) error {
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
	t.Parallel()

	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
	sock, err := wingd.SocketPath(home)
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

	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: 300 * time.Millisecond})
	sock, err := wingd.SocketPath(home)
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

func TestIdleExit_WaitsForWorkingConnections(t *testing.T) {
	t.Parallel()

	home := shortHome(t)
	const idleTimeout = 250 * time.Millisecond
	td := startDaemon(t, wingd.Config{Home: home, IdleTimeout: idleTimeout})

	cl := ensure(t, home, "")
	started := time.Now()
	select {
	case err := <-td.done:
		t.Fatalf("daemon exited while a working client was connected: %v", err)
	case <-time.After(idleTimeout + 100*time.Millisecond):
	}
	if elapsed := time.Since(started); elapsed >= 600*time.Millisecond {
		t.Fatalf("working-connection observation took %s, want under 600ms", elapsed)
	}

	_ = cl.Close()
	if err := td.waitExit(t, 3*time.Second); err != nil {
		t.Fatalf("daemon should idle out after the working client leaves, got %v", err)
	}
}

func TestIdleExit_GraceThenIdleUnderHealthProbes(t *testing.T) {
	t.Parallel()

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
	msg := readRawMessage(t, nc)
	unsupported, ok := msg.(*wingwire.Unsupported)
	if !ok {
		t.Fatalf("daemon answered an admission request on a probe connection with %T; want it refused", msg)
	}
	if unsupported.Type != string(wingwire.TypeAdmissionRequest) {
		t.Errorf("refusal names %q, want %q", unsupported.Type, wingwire.TypeAdmissionRequest)
	}
}
