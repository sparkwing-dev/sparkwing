package localcontroller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type stateFixture struct {
	wingd.FuncRunStore
	reconcile func(context.Context, store.SingletonLease) (wingd.LocalActivity, error)
	watch     func(context.Context, func(wingd.LocalActivity) error) error
}

func (*stateFixture) TypeSafeAPIKey(context.Context) (string, error) { return "", nil }

func (s *stateFixture) Reconcile(ctx context.Context, lease store.SingletonLease) (wingd.LocalActivity, error) {
	if s.reconcile != nil {
		return s.reconcile(ctx, lease)
	}
	return wingd.LocalActivity{}, nil
}

func (s *stateFixture) Watch(ctx context.Context, report func(wingd.LocalActivity) error) error {
	if s.watch != nil {
		return s.watch(ctx, report)
	}
	<-ctx.Done()
	return nil
}

type runnerPolicy func(context.Context, string) error

func (p runnerPolicy) CheckRunner(ctx context.Context, version string) error { return p(ctx, version) }

func serviceFixture(b Binding) Services {
	b.Controller.RunnerPolicy = runnerPolicy(func(context.Context, string) error { return nil })
	return Services{
		State: &stateFixture{},
		HTTP:  wingd.LocalHTTP{API: http.NotFoundHandler(), Dashboard: http.NotFoundHandler()},
		Runner: runners.StoreRunnerConfig{
			Loop:   runners.LoopConfig{Version: "v0.71.0", MaxConcurrent: 1},
			Bearer: func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() },
			Bind: func(context.Context, runners.StoreBinding) (runners.NativeServices, error) {
				return runners.NativeServices{}, errors.New("unexpected host")
			},
		},
	}
}

func openFixture(t *testing.T, bind func(context.Context, Binding) (Services, error)) *localController {
	t.Helper()
	owner, err := (Config{Bind: bind}).Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := owner.(*localController)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func seedOwner(t *testing.T, binding Binding, id string, started bool) {
	t.Helper()
	var start any
	if started {
		start = int64(1)
	}
	_, err := binding.Store.DB().ExecContext(t.Context(), `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
VALUES (?,?,(SELECT COALESCE(MAX(number),0)+1 FROM runs),'pipeline','running',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, binding.Tenant.Team(), id, []byte(`{"version":1,"request":{"origin":"local"}}`), []byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Store.DB().ExecContext(t.Context(), `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,started_at,needs_labels,prefers_labels)
VALUES (?,?,'claimed','trigger',1,1,'owner','owner-cap',7,1,?,?,?)`, binding.Tenant.Team(), id, start, []byte(`[]`), []byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompositionUsesOneStoreAndRecoversBeforeReadiness(t *testing.T) {
	var binding Binding
	c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		if b.Tenant.Team() != store.DefaultTeam || b.Controller.Store != b.Store {
			t.Fatal("shared controller did not receive the owned default-team handle")
		}
		seedOwner(t, b, "unstarted", false)
		seedOwner(t, b, "started", true)
		services := serviceFixture(b)
		services.State.(*stateFixture).reconcile = func(ctx context.Context, lease store.SingletonLease) (wingd.LocalActivity, error) {
			if lease.Name != "controller.reaper" || lease.Generation < 1 {
				return wingd.LocalActivity{}, errors.New("projection did not receive the elected fence")
			}
			started, err := b.Tenant.ReadRun(ctx, "started")
			if err != nil || started.State != store.RunFailed || started.FinishedAt == nil {
				return wingd.LocalActivity{}, fmt.Errorf("started orphan was not recovered: %+v: %w", started, err)
			}
			var status string
			if err := b.Store.DB().QueryRowContext(ctx, `SELECT status FROM triggers WHERE team=? AND id='unstarted'`, b.Tenant.Team()).Scan(&status); err != nil {
				return wingd.LocalActivity{}, err
			}
			if status != "pending" {
				return wingd.LocalActivity{}, fmt.Errorf("unstarted owner status: %s", status)
			}
			return wingd.LocalActivity{QueuedTriggers: 1}, nil
		}
		services.Close = func() error { return b.Store.DB().PingContext(t.Context()) }
		return services, nil
	})
	activity, err := c.Reconcile(t.Context())
	if err != nil || activity.QueuedTriggers != 1 {
		t.Fatalf("reconciliation: %+v: %v", activity, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := binding.Store.DB().PingContext(t.Context()); err == nil {
		t.Fatal("owned store remained open")
	}
	reopened, err := store.Open(filepath.Join(binding.Home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	tenant, err := reopened.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	row, err := tenant.ReadRun(t.Context(), "started")
	if err != nil || row.State != store.RunFailed {
		t.Fatalf("reopened orphan: %+v: %v", row, err)
	}
}

func TestCompositionCleansPartialBindings(t *testing.T) {
	for _, mode := range []string{"bind_error", "cancelled", "incomplete", "runner", "policy", "foreign_store"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure, closeFailure := errors.New("bind failed"), errors.New("services close failed")
			var owned *store.Store
			var closed int
			_, err := (Config{Bind: func(_ context.Context, b Binding) (Services, error) {
				owned = b.Store
				services := serviceFixture(b)
				services.Close = func() error {
					closed++
					return errors.Join(closeFailure, b.Store.DB().PingContext(t.Context()))
				}
				switch mode {
				case "bind_error":
					return services, failure
				case "cancelled":
					cancel()
				case "incomplete":
					services.HTTP.API = nil
				case "runner":
					services.Runner.Bind = nil
				case "policy":
					b.Controller.RunnerPolicy = nil
				case "foreign_store":
					b.Controller.Store = nil
				}
				return services, nil
			}}).Open(ctx, t.TempDir())
			if err == nil || !errors.Is(err, closeFailure) || closed != 1 || owned.DB().PingContext(t.Context()) == nil {
				t.Fatalf("partial binding leaked or lost cleanup: close=%d: %v", closed, err)
			}
			if mode == "bind_error" && !errors.Is(err, failure) || mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost startup failure: %v", err)
			}
		})
	}
}

func TestCompositionJoinsQueueAndActivityBeforeStoreClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("activity failed")
		started, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var closeCount atomic.Int32
		c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
			services := serviceFixture(b)
			services.Runner.Bearer = func(ctx context.Context) (string, error) {
				close(started)
				<-ctx.Done()
				<-release
				defer close(joined)
				return "", errors.Join(ctx.Err(), b.Store.DB().PingContext(t.Context()))
			}
			services.State.(*stateFixture).watch = func(ctx context.Context, report func(wingd.LocalActivity) error) error {
				<-started
				if err := report(wingd.LocalActivity{Runs: 1}); err != nil {
					return err
				}
				return failure
			}
			services.Close = func() error { closeCount.Add(1); return nil }
			return services, nil
		})
		if _, err := c.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		var reported wingd.LocalActivity
		go func() { result <- c.Run(t.Context(), func(a wingd.LocalActivity) error { reported = a; return nil }) }()
		synctest.Wait()
		if err := c.Close(); err == nil || closeCount.Load() != 0 {
			t.Fatal("store close ran before the queue joined")
		}
		close(release)
		if err := <-result; !errors.Is(err, failure) {
			t.Fatalf("lost activity failure: %v", err)
		}
		<-joined
		if reported.Runs != 1 {
			t.Fatal("activity did not reach the runtime")
		}
		for range 2 {
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if closeCount.Load() != 1 {
			t.Fatal("shared services closed more than once")
		}
	})
}

func FuzzLocalReconciliationKeepsFence(f *testing.F) {
	f.Add(true, uint8(0))
	f.Add(false, uint8(7))
	f.Fuzz(func(t *testing.T, expire bool, replacement uint8) {
		var queued atomic.Bool
		c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
			services := serviceFixture(b)
			services.Runner.Bearer = func(context.Context) (string, error) { queued.Store(true); return "", errors.New("unexpected claim") }
			services.State.(*stateFixture).reconcile = func(ctx context.Context, _ store.SingletonLease) (wingd.LocalActivity, error) {
				var err error
				if expire {
					_, err = b.Store.DB().ExecContext(ctx, `UPDATE leases SET until=1 WHERE name='controller.reaper'`)
				} else {
					_, err = b.Store.DB().ExecContext(ctx, `UPDATE leases SET holder='replacement',generation=generation+? WHERE name='controller.reaper'`, int64(replacement)+1)
				}
				return wingd.LocalActivity{}, err
			}
			return services, nil
		})
		if _, err := c.Reconcile(t.Context()); !errors.Is(err, store.ErrLockHeld) {
			t.Fatalf("startup accepted a lost reaper fence: %v", err)
		}
		if err := c.Run(t.Context(), func(wingd.LocalActivity) error { return nil }); err == nil || queued.Load() {
			t.Fatalf("queue started after reconciliation lost its fence: %v", err)
		}
	})
}
