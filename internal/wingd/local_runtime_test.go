package wingd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type runtimeControllerFixture struct {
	FuncRunStore
	home       string
	reconcile  func(context.Context) (LocalActivity, error)
	background func(context.Context, func(LocalActivity) error) error
	http       func() LocalHTTP
	closed     atomic.Bool
	closeErr   error
	key        func(context.Context) (string, error)
}

func (f *runtimeControllerFixture) TypeSafeAPIKey(ctx context.Context) (string, error) {
	if f.key != nil {
		return f.key(ctx)
	}
	return "", nil
}

func (f *runtimeControllerFixture) Reconcile(ctx context.Context) (LocalActivity, error) {
	if f.reconcile != nil {
		return f.reconcile(ctx)
	}
	return LocalActivity{}, nil
}

func (f *runtimeControllerFixture) Run(ctx context.Context, report func(LocalActivity) error) error {
	if f.background != nil {
		return f.background(ctx, report)
	}
	<-ctx.Done()
	return nil
}

func (f *runtimeControllerFixture) HTTP() LocalHTTP {
	if f.http != nil {
		return f.http()
	}
	return LocalHTTP{API: http.NotFoundHandler(), Dashboard: http.NotFoundHandler()}
}

func (f *runtimeControllerFixture) Close() error {
	held, err := LockHeld(f.home)
	if err != nil {
		return err
	}
	if !held {
		return errors.New("store closed after ownership was released")
	}
	f.closed.Store(true)
	return f.closeErr
}

type runtimeListener struct {
	closed chan struct{}
	once   sync.Once
}

type failedRuntimeListener struct {
	net.Listener
	failure error
}

func (l *failedRuntimeListener) Accept() (net.Conn, error) { return nil, l.failure }

type drainingRuntimeListener struct {
	net.Listener
	once              sync.Once
	stopping, release chan struct{}
	failure           error
}

func (l *drainingRuntimeListener) Close() error {
	l.once.Do(func() { close(l.stopping) })
	<-l.release
	return errors.Join(l.Listener.Close(), l.failure)
}

func (l *runtimeListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *runtimeListener) Close() error              { l.once.Do(func() { close(l.closed) }); return nil }
func (l *runtimeListener) Addr() net.Addr            { return &net.UnixAddr{Name: "runtime-fixture", Net: "unix"} }

func newRuntimeFixture(t *testing.T, controller *runtimeControllerFixture) *localRuntime {
	t.Helper()
	controller.home = t.TempDir()
	runtime, err := NewLocalRuntime(LocalRuntimeConfig{Admission: Config{Home: controller.home, Sampler: runtimeSampler{}}, Open: func(_ context.Context, home string) (LocalController, error) {
		held, err := LockHeld(home)
		if err != nil || !held {
			return nil, errors.New("store opened before ownership")
		}
		return controller, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := runtime.(*localRuntime)
	r.listen = func(string, string) (net.Listener, error) { return &runtimeListener{closed: make(chan struct{})}, nil }
	return r
}

type runtimeSampler struct{}

func (runtimeSampler) Sample() (HostStat, error) {
	return HostStat{TotalCores: 4, TotalMemoryBytes: 8 << 30, FreeMemoryBytes: 8 << 30}, nil
}

func TestLocalRuntimeOwnsStoreUntilAllServicesJoin(t *testing.T) {
	f := &runtimeControllerFixture{}
	joined := make(chan struct{})
	f.background = func(ctx context.Context, _ func(LocalActivity) error) error {
		<-ctx.Done()
		if f.closed.Load() {
			return errors.New("store closed before consumer joined")
		}
		close(joined)
		return nil
	}
	r := newRuntimeFixture(t, f)
	lifecycle := NewLocalLifecycle(r, nil)
	status, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Stay: true, DashboardAddress: "127.0.0.1:0"})
	if err != nil || !status.Running || !status.Stay || status.PID == 0 {
		t.Fatalf("start: %+v, %v", status, err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-joined
	if !f.closed.Load() {
		t.Fatal("store not closed")
	}
	if held, err := LockHeld(f.home); err != nil || held {
		t.Fatalf("ownership after shutdown: %v, %v", held, err)
	}
}

func TestLocalRuntimeRefusesConcurrentOwnerBeforeOpeningStore(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	d, err := New(Config{Home: f.home})
	if err != nil {
		t.Fatal(err)
	}
	won, err := d.elect()
	if err != nil || !won {
		t.Fatalf("election: %v %v", won, err)
	}
	defer d.releaseLock()
	if err := r.Run(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Standalone: true}); !errors.Is(err, ErrNotElected) {
		t.Fatalf("second owner: %v", err)
	}
	if f.closed.Load() {
		t.Fatal("losing election opened services")
	}
	lifecycle := NewLocalLifecycle(r, nil)
	if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1"}); !errors.Is(err, ErrNotElected) {
		t.Fatalf("lost on-demand election: %v", err)
	}
}

func TestLocalRuntimeReadinessWaitsForReconciliation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		f := &runtimeControllerFixture{reconcile: func(ctx context.Context) (LocalActivity, error) {
			close(entered)
			select {
			case <-release:
				return LocalActivity{Runs: 1}, nil
			case <-ctx.Done():
				return LocalActivity{}, ctx.Err()
			}
		}}
		r := newRuntimeFixture(t, f)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- r.Run(ctx, LocalStartOptions{Home: f.home, Version: "v1"}) }()
		<-entered
		ready := make(chan error, 1)
		go func() { _, err := r.AwaitReady(ctx); ready <- err }()
		synctest.Wait()
		select {
		case err := <-ready:
			t.Fatalf("ready before reconciliation: %v", err)
		default:
		}
		close(release)
		if err := <-ready; err != nil {
			t.Fatal(err)
		}
		status, err := r.Status(ctx)
		if err != nil || status.Activity.Runs != 1 {
			t.Fatalf("status: %+v %v", status, err)
		}
		if err := r.Drain(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestLocalRuntimeStartupFailuresReleaseOwnership(t *testing.T) {
	for _, stage := range []string{"reconcile", "api_bind", "api_serve", "dashboard_bind", "consumer"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New(stage + " failed")
			f := &runtimeControllerFixture{}
			r := newRuntimeFixture(t, f)
			switch stage {
			case "reconcile":
				f.reconcile = func(context.Context) (LocalActivity, error) { return LocalActivity{}, failure }
			case "api_bind":
				r.listen = func(network, address string) (net.Listener, error) {
					if network == "unix" && address == r.layout.apiSock {
						return nil, failure
					}
					return &runtimeListener{closed: make(chan struct{})}, nil
				}
			case "api_serve":
				r.listen = func(network, address string) (net.Listener, error) {
					listener := &runtimeListener{closed: make(chan struct{})}
					if network == "unix" && address == r.layout.apiSock {
						return &failedRuntimeListener{Listener: listener, failure: failure}, nil
					}
					return listener, nil
				}
			case "dashboard_bind":
				r.listen = func(network, _ string) (net.Listener, error) {
					if network == "tcp" {
						return nil, failure
					}
					return &runtimeListener{closed: make(chan struct{})}, nil
				}
			case "consumer":
				f.background = func(context.Context, func(LocalActivity) error) error { return failure }
			}
			err := r.Run(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Stay: true, DashboardAddress: "127.0.0.1:0"})
			if !errors.Is(err, failure) {
				t.Fatalf("failure: %v", err)
			}
			if !f.closed.Load() {
				t.Fatal("store leaked")
			}
			if held, err := LockHeld(f.home); err != nil || held {
				t.Fatalf("ownership: %v %v", held, err)
			}
		})
	}
}

func TestLocalRuntimeWrongHomeNeverOpensIt(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	if err := r.Run(t.Context(), LocalStartOptions{Home: t.TempDir(), Version: "v1"}); err == nil {
		t.Fatal("accepted another home")
	}
	if f.closed.Load() {
		t.Fatal("opened wrong home")
	}
}

func TestLocalRuntimeIdleHoldsAndProbes(t *testing.T) {
	for _, initial := range []LocalActivity{{Runs: 1}, {QueuedTriggers: 1}, {HeldLeases: 1}, {DashboardConnections: 1}} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				update := make(chan LocalActivity)
				updated := make(chan struct{})
				f := &runtimeControllerFixture{
					reconcile: func(context.Context) (LocalActivity, error) { return initial, nil },
					background: func(ctx context.Context, report func(LocalActivity) error) error {
						for {
							select {
							case value := <-update:
								if err := report(value); err != nil {
									return err
								}
								updated <- struct{}{}
							case <-ctx.Done():
								return nil
							}
						}
					},
				}
				r := newRuntimeFixture(t, f)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- r.Run(ctx, LocalStartOptions{Home: f.home, Version: "v1"}) }()
				if _, err := r.AwaitReady(ctx); err != nil {
					t.Fatal(err)
				}
				if r.run.daemon.idleElapsed() != 0 {
					t.Fatal("activity did not hold controller")
				}
				update <- LocalActivity{}
				<-updated
				status, err := r.Status(ctx)
				if err != nil {
					t.Fatal(err)
				}
				idleSince := status.Activity.IdleSince
				probed := make(chan struct{})
				stopProbe := make(chan struct{})
				defer func() { close(stopProbe); <-probed }()
				go func() {
					defer close(probed)
					ticker := time.NewTicker(limits.LocalControllerIdleTimeout / 8)
					defer ticker.Stop()
					for {
						select {
						case <-ticker.C:
							status, err := r.Status(ctx)
							if err != nil {
								t.Error(err)
								return
							}
							if status.Running && status.Activity.IdleSince != idleSince {
								t.Error("probe reset idle deadline")
								return
							}
						case <-stopProbe:
							return
						}
					}
				}()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if !f.closed.Load() {
					t.Fatal("idle exit leaked store")
				}
				if time.Since(idleSince) != limits.LocalControllerIdleTimeout {
					t.Fatalf("idle exit after %s", time.Since(idleSince))
				}
			})
		})
	}
}

func TestLocalRuntimeJoinsAPIAndPreservesShutdownFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		apiStopping, release := make(chan struct{}), make(chan struct{})
		failure := errors.New("final API flush failed")
		closeFailure := errors.New("store close failed")
		f := &runtimeControllerFixture{closeErr: closeFailure}
		r := newRuntimeFixture(t, f)
		r.listen = func(network, address string) (net.Listener, error) {
			listener := &runtimeListener{closed: make(chan struct{})}
			if network == "unix" && address == r.layout.apiSock {
				return &drainingRuntimeListener{Listener: listener, stopping: apiStopping, release: release, failure: failure}, nil
			}
			return listener, nil
		}
		lifecycle := NewLocalLifecycle(r, nil)
		if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Stay: true}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- lifecycle.Stop(t.Context()) }()
		<-apiStopping
		synctest.Wait()
		if f.closed.Load() {
			t.Fatal("store closed while API drains")
		}
		close(release)
		if err := <-done; !errors.Is(err, failure) || !errors.Is(err, closeFailure) {
			t.Fatalf("shutdown failures: %v", err)
		}
	})
}

func TestLocalRuntimeFailedStartupCanRetry(t *testing.T) {
	f := &runtimeControllerFixture{}
	r := newRuntimeFixture(t, f)
	failed := true
	f.reconcile = func(context.Context) (LocalActivity, error) {
		if failed {
			failed = false
			return LocalActivity{}, errors.New("store temporarily unavailable")
		}
		return LocalActivity{}, nil
	}
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1", Stay: true}
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err == nil {
		t.Fatal("startup failure hidden")
	}
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRuntimeCancellationJoinsReconciliation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		f := &runtimeControllerFixture{reconcile: func(ctx context.Context) (LocalActivity, error) {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return LocalActivity{}, ctx.Err()
		}}
		r := newRuntimeFixture(t, f)
		lifecycle := NewLocalLifecycle(r, nil)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := lifecycle.EnsureStarted(ctx, LocalStartOptions{Home: f.home, Version: "v1"})
			done <- err
		}()
		<-entered
		cancel()
		<-cancelled
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("returned while reconciliation still uses store: %v", err)
		default:
		}
		if f.closed.Load() {
			t.Fatal("closed store before reconciliation joined")
		}
		if held, err := LockHeld(f.home); err != nil || !held {
			t.Fatalf("lost ownership during cleanup: %v %v", held, err)
		}
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("startup cancellation: %v", err)
		}
		if !f.closed.Load() {
			t.Fatal("store not closed after cancellation")
		}
	})
}

func TestLocalRuntimeLoadsJevKeyUnderElection(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			f := &runtimeControllerFixture{}
			r := newRuntimeFixture(t, f)
			policy := DefaultAdmissionPolicy()
			policy.Mode = admission.ModeJev
			r.cfg.Admission.AdmissionPolicy = &policy
			var logs strings.Builder
			r.cfg.Admission.Logf = func(format string, args ...any) { fmt.Fprintf(&logs, format, args...) }
			reads := 0
			f.key = func(context.Context) (string, error) {
				reads++
				if held, err := LockHeld(f.home); err != nil || !held {
					t.Fatalf("key read before election: %v %v", held, err)
				}
				if missing {
					return "", errors.New("secret unavailable")
				}
				return "private-key\n", nil
			}
			lifecycle := NewLocalLifecycle(r, nil)
			if _, err := lifecycle.EnsureStarted(t.Context(), LocalStartOptions{Home: f.home, Version: "v1", Stay: true}); err != nil {
				t.Fatal(err)
			}
			advisor := r.run.daemon.jevAdvisor
			if err := lifecycle.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			if reads != 1 {
				t.Fatalf("key reads=%d", reads)
			}
			if missing {
				if advisor != nil || !strings.Contains(logs.String(), "TYPESAFE_API_KEY") {
					t.Fatalf("missing key must log and keep admission fallback: %T %s", advisor, logs.String())
				}
			} else if advisor == nil {
				t.Fatal("configured Jev lost its advisor")
			}
			if strings.Contains(logs.String(), "private-key") {
				t.Fatal("logged the admission key")
			}
		})
	}
}

func TestLocalRuntimeReadinessDoesNotWaitForConsumerPreparation(t *testing.T) {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	signal.Stop(interrupt)
	synctest.Test(t, func(t *testing.T) {
		preparing := make(chan struct{})
		f := &runtimeControllerFixture{background: func(ctx context.Context, _ func(LocalActivity) error) error {
			close(preparing)
			<-ctx.Done()
			return nil
		}}
		r := newRuntimeFixture(t, f)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- r.Run(ctx, LocalStartOptions{Home: f.home, Version: "v1"}) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
		<-preparing
		ready := make(chan LocalStatus, 1)
		go func() {
			status, err := r.AwaitReady(ctx)
			if err != nil {
				t.Error(err)
			}
			ready <- status
		}()
		synctest.Wait()
		select {
		case status := <-ready:
			if !status.Running {
				t.Fatal("controller is not ready during consumer preparation")
			}
		default:
			t.Fatal("controller readiness waits on consumer preparation")
		}
	})
}
