package wingd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestPrivateControllerIdleExitAndNextClient(t *testing.T) {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	signal.Stop(interrupt)
	synctest.Test(t, func(t *testing.T) {
		f := &runtimeControllerFixture{}
		r := newRuntimeFixture(t, f)
		lifecycle := NewLocalLifecycle(r, nil)
		opts := LocalStartOptions{Home: f.home, Version: "v1"}
		if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
			t.Fatal(err)
		}
		first := r.run
		time.Sleep(19 * time.Minute)
		synctest.Wait()
		if status, _ := lifecycle.Status(t.Context()); !status.Running {
			t.Fatal("stopped before idle bound")
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if status, _ := lifecycle.Status(t.Context()); status.Running || !f.closed.Load() {
			t.Fatal("private controller did not exit and close its store")
		}
		if held, err := LockHeld(f.home); err != nil || held {
			t.Fatalf("idle exit retained election: %t %v", held, err)
		}
		f.closed.Store(false)
		if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
			t.Fatal(err)
		}
		if r.run == first {
			t.Fatal("next client reused the stopped session")
		}
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPrivateControllerBusyResetsIdleBound(t *testing.T) {
	for _, kind := range []string{"run", "queue", "held", "dashboard", "cli", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &runtimeControllerFixture{}
				r := newRuntimeFixture(t, f)
				lifecycle := NewLocalLifecycle(r, nil)
				if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); err != nil {
					t.Fatal(err)
				}
				d := r.run.daemon
				activity := LocalActivity{}
				legacy := &conn{}
				d.mu.Lock()
				switch kind {
				case "run":
					activity.Runs = 1
				case "queue":
					activity.QueuedTriggers = 1
				case "held":
					activity.HeldLeases = 1
				case "dashboard":
					d.dashboardConns = 1
				case "cli":
					d.idleClients = map[net.Conn]idleClient{nil: {}}
				case "legacy":
					d.conns[legacy] = struct{}{}
				}
				d.mu.Unlock()
				if err := d.setLocalActivity(activity); err != nil {
					t.Fatal(err)
				}
				time.Sleep(21 * time.Minute)
				synctest.Wait()
				if status, _ := r.Status(t.Context()); !status.Running {
					t.Fatal("busy controller exited")
				}
				d.mu.Lock()
				d.dashboardConns = 0
				d.idleClients = nil
				delete(d.conns, legacy)
				d.mu.Unlock()
				if err := d.setLocalActivity(LocalActivity{}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(19 * time.Minute)
				synctest.Wait()
				if status, _ := r.Status(t.Context()); !status.Running {
					t.Fatal("busy time counted toward idle bound")
				}
				time.Sleep(2 * time.Minute)
				synctest.Wait()
				if status, _ := r.Status(t.Context()); status.Running {
					t.Fatal("controller remained after work ended")
				}
			})
		})
	}
}

func TestDefaultControllerNeverIdleExits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	synctest.Test(t, func(t *testing.T) {
		f := &runtimeControllerFixture{home: filepath.Join(home, ".sparkwing")}
		r := newRuntimeFixture(t, f)
		lifecycle := NewLocalLifecycle(r, nil)
		if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Hour)
		synctest.Wait()
		if status, _ := r.Status(t.Context()); !status.Running {
			t.Fatal("default controller exited")
		}
	})
}

func TestPrivateIdleRetiresRunnerAndLogsHome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &runtimeControllerFixture{}
		r := newRuntimeFixture(t, f)
		registry := &runtimeRetirement{controller: f, runnerHTTPFixture: runnerHTTPFixture{instance: localhostwire.LocalRunnerInstance{ID: "runner", PID: os.Getpid(), ProcessBirth: "retired-incarnation", State: "active"}}}
		r.cfg.Open = func(context.Context, string) (LocalController, error) {
			return &retiringRuntimeController{f, registry}, nil
		}
		var logs strings.Builder
		r.cfg.Admission.Logf = func(format string, args ...any) { fmt.Fprintf(&logs, format+"\n", args...) }
		if _, err := NewLocalLifecycle(r, nil).EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(21 * time.Minute)
		synctest.Wait()
		if !registry.drained || !f.closed.Load() {
			t.Fatal("idle exit did not retire runner before closing controller")
		}
		if !strings.Contains(logs.String(), "private controller idle exit: home="+f.home+" idle=20m0s") {
			t.Fatalf("missing idle exit log: %s", logs.String())
		}
	})
}

func TestPrivateIdleAPIConnections(t *testing.T) {
	for _, path := range []string{"/api/v1/runs", "/api/v1/runners/claim", localRunnerPath + "/runner", controllerHTTPPath + "probe"} {
		t.Run(path, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d, err := New(Config{Home: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				d.idleSince = time.Now()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				listener := &localHTTPListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
				client, server := net.Pipe()
				defer client.Close()
				owner, err := controllerPeerIdentity()
				if err != nil {
					t.Fatal(err)
				}
				listener.connections <- localHTTPConnection{Conn: server, peer: owner}
				done := make(chan error, 1)
				go func() {
					done <- d.serveIdleAPI(ctx, listener, localhostwire.LocalHTTP{API: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })})
				}()
				joined := make(chan struct{})
				go func() { defer close(joined); d.watchPrivateIdle(ctx) }()
				// bug: runner connections also create credentials before they start polling.
				reader := bufio.NewReader(client)
				for _, requestPath := range []string{"/api/v1/tokens", path} {
					if _, err := fmt.Fprintf(client, "GET %s HTTP/1.1\r\nHost: localhost\r\n\r\n", requestPath); err != nil {
						t.Fatal(err)
					}
					response, err := http.ReadResponse(reader, nil)
					if err != nil {
						t.Fatal(err)
					}
					response.Body.Close()
				}
				time.Sleep(21 * time.Minute)
				synctest.Wait()
				select {
				case <-d.quit:
					if !idleRunnerRequest(path) {
						t.Fatal("connected client lost its controller")
					}
				default:
					if idleRunnerRequest(path) {
						t.Fatal("idle runner control traffic prevented exit")
					}
					client.Close()
					time.Sleep(21 * time.Minute)
					synctest.Wait()
					select {
					case <-d.quit:
					default:
						t.Fatal("disconnected client kept controller alive")
					}
				}
				if idleRunnerRequest(path) {
					if _, err := fmt.Fprint(client, "POST /api/v1/claims/work/finish HTTP/1.1\r\nHost: localhost\r\nContent-Length: 0\r\n\r\n"); err != nil {
						t.Fatal(err)
					}
					response, err := http.ReadResponse(reader, nil)
					if err != nil {
						t.Fatal(err)
					}
					response.Body.Close()
					if response.StatusCode != http.StatusNoContent {
						t.Fatal("retiring controller refused a worker result")
					}
				}
				cancel()
				<-joined
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

type idleActivityController struct {
	*runtimeControllerFixture
	activity func(context.Context) (LocalActivity, error)
}

func (c *idleActivityController) LocalIdleActivity(ctx context.Context) (LocalActivity, error) {
	return c.activity(ctx)
}

func TestPrivateIdleFreshActivityRefusesErrorsAndWork(t *testing.T) {
	for _, failure := range []bool{true, false} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &runtimeControllerFixture{}
				r := newRuntimeFixture(t, f)
				busy := true
				r.cfg.Open = func(context.Context, string) (LocalController, error) {
					return &idleActivityController{f, func(context.Context) (LocalActivity, error) {
						if busy {
							if failure {
								return LocalActivity{}, errors.New("snapshot unavailable")
							}
							return LocalActivity{Runs: 1}, nil
						}
						return LocalActivity{}, nil
					}}, nil
				}
				if _, err := NewLocalLifecycle(r, nil).EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(21 * time.Minute)
				synctest.Wait()
				if status, _ := r.Status(t.Context()); !status.Running {
					t.Fatal("stale idle report retired controller")
				}
				busy = false
				time.Sleep(20 * time.Minute)
				synctest.Wait()
				if status, _ := r.Status(t.Context()); status.Running {
					t.Fatal("fresh idle snapshot did not permit retirement")
				}
			})
		})
	}
}
