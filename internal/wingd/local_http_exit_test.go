package wingd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
)

func TestLocalHTTPListenerFailureCancelsAndJoinsRequests(t *testing.T) {
	for _, api := range []bool{false, true} {
		for _, hijack := range []bool{false, true} {
			name := map[bool]string{false: "dashboard", true: "api"}[api] + map[bool]string{false: "/http", true: "/hijacked"}[hijack]
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					listener := &localHTTPListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
					client, server := net.Pipe()
					defer client.Close()
					uid := uint32(123)
					listener.connections <- localHTTPConnection{Conn: server, peer: LocalPeerIdentity{GOOS: "linux", UID: &uid}}
					entered, exited := make(chan struct{}), make(chan struct{})
					handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						defer close(exited)
						if hijack {
							connection, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							defer connection.Close()
						}
						close(entered)
						<-r.Context().Done()
					})
					services := LocalHTTP{API: handler, Dashboard: handler}
					done := make(chan error, 1)
					go func() {
						if api {
							done <- services.ServeAPI(ctx, listener)
						} else {
							done <- ServeLocalHTTP(ctx, listener, services.Dashboard, nil, nil)
						}
					}()
					if _, err := io.WriteString(client, "GET / HTTP/1.1\r\nHost: local\r\nConnection: close\r\n\r\n"); err != nil {
						t.Fatal(err)
					}
					<-entered
					read := make(chan error, 1)
					go func() { _, err := io.Copy(io.Discard, client); read <- err }()
					if err := listener.Close(); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					select {
					case <-exited:
					default:
						t.Error("listener failure left the handler's request context live")
						cancel()
					}
					if err := <-done; !errors.Is(err, net.ErrClosed) {
						t.Fatalf("listener failure lost during shutdown: %v", err)
					}
					if err := <-read; err != nil {
						t.Fatal(err)
					}
					if ctx.Err() != nil {
						t.Error("HTTP exit required cancellation of its caller")
					}
				})
			})
		}
	}
}

func TestLocalRuntimeListenerFailureJoinsRequestsBeforeStoreClose(t *testing.T) {
	f := &runtimeControllerFixture{}
	entered, cancelled, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cleanup, finish := context.WithCancel(t.Context())
	defer finish()
	f.http = func() LocalHTTP {
		return LocalHTTP{API: http.NotFoundHandler(), Dashboard: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(exited)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer connection.Close()
			close(entered)
			<-r.Context().Done()
			close(cancelled)
			<-cleanup.Done()
			if f.closed.Load() {
				t.Error("store closed before the handler finished")
			}
		})}
	}
	r := newRuntimeFixture(t, f)
	listener := &localHTTPListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
	r.listen = func(network, _ string) (net.Listener, error) {
		if network == "tcp" {
			return listener, nil
		}
		return &runtimeListener{closed: make(chan struct{})}, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.StartDashboard(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Stay: true, DashboardAddress: "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	done := r.run.done
	r.mu.Unlock()
	client, server := net.Pipe()
	defer client.Close()
	listener.connections <- server
	if _, err := io.WriteString(client, "GET / HTTP/1.1\r\nHost: dashboard\r\nAuthorization: "+signedInBearer(t, r)+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	if f.closed.Load() {
		t.Fatal("listener failure closed the store before handler cleanup")
	}
	finish()
	<-exited
	<-done
	if err := lifecycle.Stop(t.Context()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener failure lost during runtime shutdown: %v", err)
	}
	if !f.closed.Load() {
		t.Fatal("runtime returned without closing its store")
	}
	if held, err := LockHeld(f.home); err != nil || held {
		t.Fatalf("ownership after listener failure: %v, %v", held, err)
	}
}
