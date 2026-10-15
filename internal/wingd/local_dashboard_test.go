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
)

func TestLocalDashboardStartsOnExistingController(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	var binds atomic.Int32
	var dashboard *runtimeListener
	r.listen = func(network, _ string) (net.Listener, error) {
		listener := &runtimeListener{closed: make(chan struct{})}
		if network == "tcp" {
			binds.Add(1)
			dashboard = listener
		}
		return listener, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1", Stay: true}
	first, err := lifecycle.EnsureStarted(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if binds.Load() != 0 {
		t.Fatal("dashboard started without a request")
	}
	opts.DashboardAddress = "127.0.0.1:0"
	for range 2 {
		status, err := lifecycle.StartDashboard(t.Context(), opts)
		if err != nil || !status.Running || status.PID != first.PID {
			t.Fatalf("dashboard start: %+v %v", status, err)
		}
	}
	if binds.Load() != 1 {
		t.Fatalf("dashboard binds=%d, want one", binds.Load())
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dashboard.closed:
	default:
		t.Fatal("dashboard listener survived controller shutdown")
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
	opts := LocalStartOptions{Home: f.home, Version: "v1", Stay: true}
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	opts.DashboardAddress = "127.0.0.1:0"
	if _, err := lifecycle.StartDashboard(t.Context(), opts); !errors.Is(err, failure) {
		t.Fatalf("bind error: %v", err)
	}
	status, err := lifecycle.Status(t.Context())
	if err != nil || !status.Running || f.closed.Load() {
		t.Fatalf("controller lost after bind failure: %+v %v", status, err)
	}
}

func TestLocalDashboardStartupFailureReachesCaller(t *testing.T) {
	f := &runtimeControllerFixture{http: func() LocalHTTP { return LocalHTTP{API: http.NotFoundHandler()} }}
	r := newRuntimeFixture(t, f)
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1", Stay: true}
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	opts.DashboardAddress = "127.0.0.1:0"
	if _, err := lifecycle.StartDashboard(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "shared handler") {
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
		f.http = func() LocalHTTP {
			if calls.Add(1) == 2 {
				close(entered)
				<-accept
			}
			return LocalHTTP{API: http.NotFoundHandler(), Dashboard: http.NotFoundHandler()}
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
		opts := LocalStartOptions{Home: f.home, Version: "v1", Stay: true}
		if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
			t.Fatal(err)
		}
		opts.DashboardAddress = "127.0.0.1:0"
		started := make(chan error, 1)
		go func() { _, err := lifecycle.StartDashboard(t.Context(), opts); started <- err }()
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
