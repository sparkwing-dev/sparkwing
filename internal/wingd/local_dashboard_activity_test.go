package wingd

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestLocalRuntimeDashboardConnectionsHoldIdle(t *testing.T) {
	// safety: Go's process-wide signal goroutine must start outside a synctest bubble.
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	signal.Stop(interrupt)
	for _, hijack := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "hijacked"}[hijack], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &runtimeControllerFixture{}
				entered := make(chan struct{})
				f.http = func() LocalHTTP {
					return LocalHTTP{API: http.NotFoundHandler(), Dashboard: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						if !hijack {
							w.WriteHeader(http.StatusNoContent)
							return
						}
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer connection.Close()
						close(entered)
						_, _ = connection.Read(make([]byte, 1))
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
				_, err := lifecycle.StartDashboard(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", DashboardAddress: "127.0.0.1:0"})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := lifecycle.Stop(context.Background()); err != nil {
						t.Error(err)
					}
				}()
				client, server := net.Pipe()
				defer client.Close()
				listener.connections <- server
				if _, err := io.WriteString(client, "GET / HTTP/1.1\r\nHost: dashboard\r\nCookie: "+signedInCookie(t, r)+"\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				if hijack {
					<-entered
				} else {
					response, err := http.ReadResponse(bufio.NewReader(client), nil)
					if err != nil {
						t.Fatal(err)
					}
					response.Body.Close()
				}
				synctest.Wait()
				status, err := r.Status(t.Context())
				if err != nil || !status.Running || status.Activity.DashboardConnections != 1 || !status.Activity.IdleSince.IsZero() {
					t.Fatalf("dashboard connection missing from activity: %+v, %v", status, err)
				}
				for _, activity := range []LocalActivity{{Runs: 1}, {}} {
					if err := r.run.daemon.SetLocalActivity(activity); err != nil {
						t.Fatal(err)
					}
					status, err = r.Status(t.Context())
					if err != nil || status.Activity.Runs != activity.Runs || status.Activity.DashboardConnections != 1 {
						t.Fatalf("controller report replaced dashboard activity: %+v, %v", status, err)
					}
				}
				<-time.NewTimer(2 * limits.LocalControllerIdleTimeout).C
				status, err = r.Status(t.Context())
				if err != nil || !status.Running {
					t.Fatalf("controller exited with an open dashboard connection: %+v, %v", status, err)
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				status, err = r.Status(t.Context())
				if err != nil || status.Activity.DashboardConnections != 0 || status.Activity.IdleSince.IsZero() {
					t.Fatalf("closed dashboard connection retained activity: %+v, %v", status, err)
				}
				<-time.NewTimer(2 * limits.LocalControllerIdleTimeout).C
				synctest.Wait()
				status, err = r.Status(t.Context())
				if err != nil || status.Running || !f.closed.Load() {
					t.Fatalf("idle controller did not join its shutdown: %+v, %v", status, err)
				}
			})
		})
	}
}
