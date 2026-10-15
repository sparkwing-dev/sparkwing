package localcontroller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// State supplies the local recovery and activity projection over the owned store.
type State interface {
	wingd.RunStore
	TypeSafeAPIKey(context.Context) (string, error)
	Reconcile(context.Context, store.SingletonLease) (wingd.LocalActivity, error)
	Watch(context.Context, func(wingd.LocalActivity) error) error
}

// Binding gives shared service constructors the elected process's only store handle.
type Binding struct {
	Home       string
	Store      *store.Store
	Tenant     *store.Tenant
	Controller *controller.EngineController
}

// NewQueue binds the shared runner and native hosts to this elected controller.
func (b Binding) NewQueue(cfg runners.StoreRunnerConfig) (*runners.Loop, error) {
	if b.Controller == nil || b.Controller.Store != b.Store || b.Store == nil {
		return nil, errors.New("wingd: queue requires the elected controller store")
	}
	return runners.NewStoreRunner(b.Controller, cfg)
}

// Services binds shared authorization, host execution and local state to one process.
type Services struct {
	State  State
	HTTP   wingd.LocalHTTP
	Runner runners.StoreRunnerConfig
	Close  func() error
}

// Config supplies shared service constructors without opening another store.
type Config struct {
	Bind func(context.Context, Binding) (Services, error)
}

// Open constructs LocalRuntimeConfig.Open's store owner under the runtime's election.
func (cfg Config) Open(ctx context.Context, home string) (_ wingd.LocalController, err error) {
	if cfg.Bind == nil {
		return nil, errors.New("wingd: shared local services are required")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	home, err = wingd.HomeDir(home)
	if err != nil {
		return nil, err
	}
	if err := fssecure.EnsureDir(home); err != nil {
		return nil, err
	}
	s, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		return nil, err
	}
	var services Services
	defer func() {
		if err != nil {
			err = errors.Join(err, closeServices(services), s.Close())
		}
	}()
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return nil, err
	}
	binding := Binding{Home: home, Store: s, Tenant: tenant, Controller: &controller.EngineController{Store: s}}
	services, err = cfg.Bind(ctx, binding)
	if err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if services.State == nil || services.HTTP.API == nil || services.HTTP.Dashboard == nil {
		return nil, errors.New("wingd: local services require state and shared HTTP handlers")
	}
	queue, err := binding.NewQueue(services.Runner)
	if err != nil {
		return nil, err
	}
	return &localController{State: services.State, services: services, store: s, queue: queue}, nil
}

type localController struct {
	State
	services Services
	store    *store.Store
	queue    *runners.Loop
	mu       sync.Mutex
	lease    store.SingletonLease
	running  bool
	closed   bool
	closeErr error
}

func (c *localController) HTTP() wingd.LocalHTTP { return c.services.HTTP }

func (c *localController) Reconcile(ctx context.Context) (wingd.LocalActivity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.running || c.lease.Name != "" {
		return wingd.LocalActivity{}, errors.New("wingd: local reconciliation requires a new controller")
	}
	lease, err := c.store.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", "wingd", limits.DefaultLease)
	if err != nil {
		return wingd.LocalActivity{}, err
	}
	if err := c.sweep(ctx, lease); err != nil {
		return wingd.LocalActivity{}, err
	}
	activity, err := c.State.Reconcile(ctx, lease)
	if err == nil {
		lease, err = c.store.AsOperator().RenewSingletonLease(ctx, lease, limits.DefaultLease)
	}
	if err == nil {
		c.lease = lease
	}
	return activity, err
}

func (c *localController) Run(ctx context.Context, report func(wingd.LocalActivity) error) error {
	c.mu.Lock()
	if c.closed || c.running || c.lease.Name == "" || report == nil {
		c.mu.Unlock()
		return errors.New("wingd: local workers require reconciled state and an activity reporter")
	}
	c.running = true
	lease := c.lease
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workers := []struct {
		name string
		run  func(context.Context) error
	}{
		{"queue", c.queue.Run},
		{"activity", func(ctx context.Context) error { return c.State.Watch(ctx, report) }},
		{"maintenance", func(ctx context.Context) error { return c.maintain(ctx, lease) }},
	}
	results := make(chan error, len(workers))
	for _, worker := range workers {
		go func() {
			err := worker.run(ctx)
			if err == nil && ctx.Err() == nil {
				err = errors.New("stopped unexpectedly")
			}
			if err != nil {
				err = fmt.Errorf("wingd: local %s: %w", worker.name, err)
			}
			results <- err
		}()
	}
	var result error
	for range workers {
		result = errors.Join(result, <-results)
		cancel()
	}
	return result
}

func (c *localController) maintain(ctx context.Context, lease store.SingletonLease) error {
	tick := time.NewTicker(limits.Renew(limits.DefaultLease))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			var err error
			lease, err = c.store.AsOperator().RenewSingletonLease(ctx, lease, limits.DefaultLease)
			if err != nil {
				return err
			}
			if err := c.sweep(ctx, lease); err != nil {
				return err
			}
		}
	}
}

func (c *localController) sweep(ctx context.Context, lease store.SingletonLease) error {
	const batch = 100
	for {
		units, err := c.store.ReapUnits(ctx, lease, batch)
		if err != nil {
			return err
		}
		if len(units) < batch {
			break
		}
	}
	_, err := c.store.AsOperator().MaintainConcurrency(ctx, lease, store.ConcurrencyMaintenanceOptions{})
	return err
}

func (c *localController) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return errors.New("wingd: local workers must join before store close")
	}
	if !c.closed {
		c.closed = true
		c.closeErr = errors.Join(closeServices(c.services), c.store.Close())
	}
	return c.closeErr
}

func closeServices(services Services) error {
	if services.Close != nil {
		return services.Close()
	}
	return nil
}
