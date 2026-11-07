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

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func TestLocalDashboardBindsOnEveryStart(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	r.cfg.Dashboard = DashboardSettings{Address: "127.0.0.1:4343", ReadOnly: true}
	var binds []string
	var listeners []*runtimeListener
	r.listen = func(network, address string) (net.Listener, error) {
		listener := &runtimeListener{closed: make(chan struct{})}
		if network == "tcp" {
			binds = append(binds, address)
			listeners = append(listeners, listener)
		}
		return listener, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1"}
	for range 2 {
		status, err := lifecycle.EnsureStarted(t.Context(), opts)
		if err != nil || !status.Running || status.DashboardURL == "" || status.DashboardError != "" {
			t.Fatalf("start: %+v %v", status, err)
		}
		if err := lifecycle.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(binds) != 2 || binds[0] != "127.0.0.1:4343" || binds[1] != "127.0.0.1:4343" {
		t.Fatalf("dashboard binds=%v, want the configured address on each start", binds)
	}
	for _, listener := range listeners {
		select {
		case <-listener.closed:
		default:
			t.Fatal("dashboard listener survived controller shutdown")
		}
	}
	opts.Standalone = true
	status, err := lifecycle.EnsureStarted(t.Context(), opts)
	if err != nil || status.DashboardURL != "" || len(binds) != 2 {
		t.Fatalf("standalone controller served a dashboard: %+v %v %v", status, binds, err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalDashboardBindFailureLeavesControllerAvailable(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	failure := errors.New("dashboard address is occupied")
	r.listen = func(network, _ string) (net.Listener, error) {
		if network == "tcp" {
			return nil, failure
		}
		return &runtimeListener{closed: make(chan struct{})}, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1"}
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	status, err := lifecycle.Status(t.Context())
	if err != nil || !status.Running || f.closed.Load() {
		t.Fatalf("controller lost after bind failure: %+v %v", status, err)
	}
	if status.DashboardURL != "" || !strings.Contains(status.DashboardError, failure.Error()) || !strings.Contains(status.DashboardError, r.cfg.Dashboard.Address) {
		t.Fatalf("bind failure not reported: %+v", status)
	}
}

func TestLocalDashboardStartupFailureReachesCaller(t *testing.T) {
	f := &runtimeControllerFixture{http: func() localhostwire.LocalHTTP { return localhostwire.LocalHTTP{API: http.NotFoundHandler()} }}
	r := newRuntimeFixture(t, f)
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1"}
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "shared handler") {
		t.Fatalf("startup error: %v", err)
	}
	if !f.closed.Load() {
		t.Fatal("failed service startup returned before store close")
	}
	if err := lifecycle.Stop(t.Context()); err == nil || !strings.Contains(err.Error(), "shared handler") {
		t.Fatalf("shutdown error: %v", err)
	}
}

func TestLocalDashboardReadinessAndJoinedShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, accept, closing, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		f := &runtimeControllerFixture{}
		var calls atomic.Int32
		f.http = func() localhostwire.LocalHTTP {
			// safety: the dashboard builds its handler before the API serves, so it is the first call.
			if calls.Add(1) == 1 {
				close(entered)
				<-accept
			}
			return localhostwire.LocalHTTP{API: http.NotFoundHandler(), Dashboard: http.NotFoundHandler()}
		}
		r := newRuntimeFixture(t, f)
		r.listen = func(network, _ string) (net.Listener, error) {
			listener := &runtimeListener{closed: make(chan struct{})}
			if network == "tcp" {
				return &drainingRuntimeListener{Listener: listener, stopping: closing, release: release}, nil
			}
			return listener, nil
		}
		lifecycle := NewLocalLifecycle(r, nil)
		opts := LocalStartOptions{Home: f.home, Version: "v1"}
		started := make(chan error, 1)
		go func() { _, err := lifecycle.EnsureStarted(t.Context(), opts); started <- err }()
		<-entered
		synctest.Wait()
		select {
		case err := <-started:
			t.Fatalf("dashboard ready before Accept: %v", err)
		default:
		}
		close(accept)
		if err := <-started; err != nil {
			t.Fatal(err)
		}
		stopped := make(chan error, 1)
		go func() { stopped <- lifecycle.Stop(t.Context()) }()
		<-closing
		synctest.Wait()
		select {
		case err := <-stopped:
			t.Fatalf("shutdown before dashboard joined: %v", err)
		default:
		}
		close(release)
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
	})
}
