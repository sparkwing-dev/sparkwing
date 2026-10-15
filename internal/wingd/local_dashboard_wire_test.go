package wingd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestLocalDashboardWireStartsAndReusesOwner(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	var binds atomic.Int32
	r.listen = func(network, _ string) (net.Listener, error) {
		if network == "tcp" {
			binds.Add(1)
		}
		return &runtimeListener{closed: make(chan struct{})}, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Stay: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	for range 2 {
		reply, ok := localControlPipe(t, r.run.daemon, false)(&wingwire.LocalDashboard{Address: "127.0.0.1:0"}).(*wingwire.LocalDashboardAck)
		if !ok || reply.Error != "" {
			t.Fatalf("dashboard start: %+v", reply)
		}
	}
	if binds.Load() != 1 {
		t.Fatalf("dashboard binds=%d, want one", binds.Load())
	}
	reply := localControlPipe(t, r.run.daemon, false)(&wingwire.LocalDashboard{Address: "127.0.0.1:9001"}).(*wingwire.LocalDashboardAck)
	if !strings.Contains(reply.Error, "already serves") {
		t.Fatalf("address change: %+v", reply)
	}
	status, err := r.Status(t.Context())
	if err != nil || !status.Running || !status.Stay {
		t.Fatalf("owner after refused address: %+v %v", status, err)
	}
}

func TestLocalDashboardWireRefusesProbeAndAdmissionOnly(t *testing.T) {
	d, err := New(Config{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.initLedger(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.finalShutdown)
	for _, probe := range []bool{false, true} {
		if _, ok := localControlPipe(t, d, probe)(&wingwire.LocalDashboard{Address: "127.0.0.1:0"}).(*wingwire.Unsupported); !ok {
			t.Fatalf("probe=%t: dashboard served by an admission-only daemon", probe)
		}
	}
	var calls atomic.Int32
	d.localDashboard = func(context.Context, string) error { calls.Add(1); return nil }
	if _, ok := localControlPipe(t, d, true)(&wingwire.LocalDashboard{Address: "127.0.0.1:0"}).(*wingwire.Unsupported); !ok || calls.Load() != 0 {
		t.Fatal("health probe started dashboard")
	}
}

func TestLocalDashboardWireBindFailureKeepsOwner(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	r.listen = func(network, _ string) (net.Listener, error) {
		if network == "tcp" {
			return nil, errors.New("dashboard address is occupied")
		}
		return &runtimeListener{closed: make(chan struct{})}, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Stay: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	reply := localControlPipe(t, r.run.daemon, false)(&wingwire.LocalDashboard{Address: "127.0.0.1:0"}).(*wingwire.LocalDashboardAck)
	if !strings.Contains(reply.Error, "address is occupied") {
		t.Fatalf("lost bind failure: %+v", reply)
	}
	status, err := r.Status(t.Context())
	if err != nil || !status.Running || f.closed.Load() {
		t.Fatalf("owner after failed bind: %+v %v", status, err)
	}
}

func TestLocalDashboardWirePeerDisconnectCancelsAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d, err := New(Config{Home: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.initLedger(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.finalShutdown)
		entered, returned := make(chan struct{}), make(chan struct{})
		d.localDashboard = func(ctx context.Context, _ string) error {
			defer close(returned)
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		peer, _ := localControlConnection(t, d, false)
		body, err := wingwire.Encode(&wingwire.LocalDashboard{Address: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.Write(body); err != nil {
			t.Fatal(err)
		}
		<-entered
		peer.Close()
		<-returned
		d.connections.Wait()
	})
}

func TestLocalDashboardWireServiceFailureJoinsShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		var calls atomic.Int32
		f := &runtimeControllerFixture{http: func() LocalHTTP {
			if calls.Add(1) == 2 {
				close(entered)
			}
			return LocalHTTP{API: http.NotFoundHandler()}
		}}
		r := newRuntimeFixture(t, f)
		lifecycle := NewLocalLifecycle(r, nil)
		if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Stay: true}); err != nil {
			t.Fatal(err)
		}
		peer, _ := localControlConnection(t, r.run.daemon, false)
		body, err := wingwire.Encode(&wingwire.LocalDashboard{Address: "127.0.0.1:0"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := peer.Write(body); err != nil {
			t.Fatal(err)
		}
		<-entered
		if err := lifecycle.Stop(t.Context()); err == nil || !strings.Contains(err.Error(), "shared handler") {
			t.Fatalf("service error: %v", err)
		}
		if !f.closed.Load() {
			t.Fatal("store close did not join dashboard request")
		}
	})
}
