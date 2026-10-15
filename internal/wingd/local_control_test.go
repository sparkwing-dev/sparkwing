package wingd

import (
	"bufio"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func localControlPipe(t *testing.T, d *Daemon, probe bool) func(wingwire.Message) wingwire.Message {
	t.Helper()
	peer, server := net.Pipe()
	d.startConn(newConn(d, server))
	t.Cleanup(func() { peer.Close() })
	scanner := bufio.NewScanner(peer)
	exchange := func(request wingwire.Message) wingwire.Message {
		t.Helper()
		body, err := wingwire.Encode(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.Write(body); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("missing reply: %v", scanner.Err())
		}
		reply, err := wingwire.Decode(scanner.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	if _, ok := exchange(&wingwire.Hello{ProtocolMajor: ProtocolMajor, HealthProbe: probe}).(*wingwire.HelloAck); !ok {
		t.Fatal("handshake refused")
	}
	return exchange
}

func TestLocalControlReportsOwnerAndStay(t *testing.T) {
	f := &runtimeControllerFixture{reconcile: func(context.Context) (LocalActivity, error) {
		return LocalActivity{Runs: 1, QueuedTriggers: 2, HeldLeases: 3, DashboardConnections: 4}, nil
	}}
	r := newRuntimeFixture(t, f)
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	exchange := localControlPipe(t, r.run.daemon, false)
	for _, stay := range []bool{true, false} {
		reply, ok := exchange(&wingwire.LocalControl{Stay: &stay}).(*wingwire.LocalControlStatus)
		if !ok {
			t.Fatal("missing local status")
		}
		if !reply.Running || reply.Stay != stay || reply.PID == 0 || reply.Version != "v1" {
			t.Fatalf("owner: %+v", reply)
		}
		if reply.Runs != 1 || reply.QueuedTriggers != 2 || reply.HeldLeases != 3 || reply.DashboardConnections != 4 || !reply.IdleSince.IsZero() {
			t.Fatalf("activity: %+v", reply)
		}
		status, err := r.Status(t.Context())
		if err != nil || status.Stay != stay {
			t.Fatalf("runtime disagrees: %+v %v", status, err)
		}
	}
}

func TestLocalControlProbeCannotMutateOrResetIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &runtimeControllerFixture{}
		r := newRuntimeFixture(t, f)
		var clock atomic.Pointer[time.Time]
		now := time.Now()
		clock.Store(&now)
		r.cfg.Admission.Now = func() time.Time { return *clock.Load() }
		lifecycle := NewLocalLifecycle(r, nil)
		if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := lifecycle.Stop(context.Background()); err != nil {
				t.Error(err)
			}
		})
		exchange := localControlPipe(t, r.run.daemon, true)
		before := exchange(&wingwire.LocalControl{}).(*wingwire.LocalControlStatus)
		if before.IdleSince.IsZero() {
			t.Fatal("missing idle boundary")
		}
		stay := true
		if _, ok := exchange(&wingwire.LocalControl{Stay: &stay}).(*wingwire.Unsupported); !ok {
			t.Fatal("health probe changed stay mode")
		}
		// safety: a deterministic admission clock exposes any probe that touches idle accounting.
		later := before.IdleSince.Add(time.Second)
		clock.Store(&later)
		after := exchange(&wingwire.LocalControl{}).(*wingwire.LocalControlStatus)
		if after.Stay || after.IdleSince != before.IdleSince {
			t.Fatalf("probe changed lifecycle: %+v", after)
		}
	})
}

func TestLocalControlAdmissionOnlyRefuses(t *testing.T) {
	d, err := New(Config{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.initLedger(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.finalShutdown() })
	if _, ok := localControlPipe(t, d, true)(&wingwire.LocalControl{}).(*wingwire.Unsupported); !ok {
		t.Fatal("admission-only daemon impersonated local controller")
	}
}

func TestLocalControlCannotPromoteBeforeReadiness(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := &runtimeControllerFixture{serve: func(ctx context.Context, ln net.Listener) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return serveRuntimeFixture(ctx, ln)
	}}
	r := newRuntimeFixture(t, f)
	lifecycle := NewLocalLifecycle(r, nil)
	started := make(chan error, 1)
	go func() {
		_, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"})
		started <- err
	}()
	<-entered
	exchange := localControlPipe(t, r.run.daemon, false)
	stay := true
	status := exchange(&wingwire.LocalControl{Stay: &stay}).(*wingwire.LocalControlStatus)
	if status.Running || status.Stay {
		t.Fatalf("promoted unready controller: %+v", status)
	}
	close(release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	status = exchange(&wingwire.LocalControl{}).(*wingwire.LocalControlStatus)
	if !status.Running || status.Stay {
		t.Fatalf("lost readiness or saved refused promotion: %+v", status)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalControlRefusesStayAfterIdleShutdownCommits(t *testing.T) {
	idleDecision, continueIdle := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(continueIdle)
		}
	}()
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	r.cfg.Admission.IdleTimeout = time.Millisecond
	r.cfg.Admission.Logf = func(format string, _ ...any) {
		if format == "idle for %s, exiting" {
			close(idleDecision)
			<-continueIdle
		}
	}
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	<-idleDecision
	exchange := localControlPipe(t, r.run.daemon, false)
	stay := true
	status := exchange(&wingwire.LocalControl{Stay: &stay}).(*wingwire.LocalControlStatus)
	if status.Running || status.Stay {
		t.Errorf("acknowledged stay after committing idle shutdown: %+v", status)
	}
	if err := r.SetStay(t.Context(), true); err == nil {
		t.Error("in-process caller promoted stopping controller")
	}
	close(continueIdle)
	released = true
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
