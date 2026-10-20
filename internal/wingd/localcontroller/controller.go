package localcontroller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// State supplies the local recovery and activity projection over the owned store.
type State interface {
	Ready() error
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

// Services binds shared authorization, host execution and local state to one process.
type Services struct {
	Policies store.Policies
	State    State
	HTTP     wingd.LocalHTTP
	Runner   runners.StoreRunnerConfig
	Close    func() error
	Maintain func(context.Context)
	Complete func(context.Context) error
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
	var services Services
	var binding Binding
	s, err := store.Open(filepath.Join(home, "state.db"), store.WithPolicies(func(s *store.Store) (store.Policies, error) {
		tenant, err := s.ForTeam(ctx, store.DefaultTeam)
		if err != nil {
			return store.Policies{}, err
		}
		binding = Binding{Home: home, Store: s, Tenant: tenant, Controller: controller.NewEngineController(s, controller.EngineController{})}
		services, err = cfg.Bind(ctx, binding)
		return services.Policies, err
	}))
	if err != nil {
		return nil, errors.Join(err, closeServices(services))
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeServices(services), s.Close())
		}
	}()

	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if services.State == nil || services.HTTP.API == nil || services.HTTP.Dashboard == nil {
		return nil, errors.New("wingd: local services require state and shared HTTP handlers")
	}
	queue, err := runners.NewStoreRunner(binding.Controller, services.Runner)
	if err != nil {
		return nil, err
	}
	return &localController{State: services.State, services: services, store: s, operator: s.AsOperator(), tenant: binding.Tenant, queue: queue}, nil
}

type localController struct {
	State
	services Services
	store    *store.Store
	operator *store.Operator
	tenant   *store.Tenant
	queue    *runners.Loop
	mu       sync.Mutex
	lease    store.SingletonLease
	owner    store.SingletonLease
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
	lease, err := c.operator.AcquireSingletonLease(ctx, "controller.reaper", "wingd", limits.DefaultLease)
	if err != nil {
		return wingd.LocalActivity{}, err
	}
	if err := c.sweep(ctx, lease); err != nil {
		return wingd.LocalActivity{}, err
	}
	activity, err := c.State.Reconcile(ctx, lease)
	if err == nil {
		lease, err = c.operator.RenewSingletonLease(ctx, lease, limits.DefaultLease)
	}
	var owner store.SingletonLease
	if err == nil {
		owner, err = c.ownRuns(ctx, store.SingletonLease{})
	}
	if err == nil {
		c.lease, c.owner = lease, owner
	}
	return activity, err
}

// safety: holding controller.owner is what lets this queue schedule and recover controller-dispatched runs.
func (c *localController) ownRuns(ctx context.Context, owner store.SingletonLease) (store.SingletonLease, error) {
	operator := c.operator
	var err error
	if owner.Name == "" {
		owner, err = operator.AcquireSingletonLease(ctx, "controller.owner", "wingd", limits.DefaultLease)
	} else {
		owner, err = operator.RenewSingletonLease(ctx, owner, limits.DefaultLease)
	}
	if err != nil {
		return owner, err
	}
	for after := ""; ; {
		page, err := operator.ReconcileControllerRuns(ctx, owner, after, 500)
		if err != nil || len(page) < 500 {
			return owner, err
		}
		after = page[len(page)-1].RunID
	}
}

func (c *localController) Run(ctx context.Context, report func(wingd.LocalActivity) error) error {
	c.mu.Lock()
	if c.closed || c.running || c.lease.Name == "" || report == nil {
		c.mu.Unlock()
		return errors.New("wingd: local workers require reconciled state and an activity reporter")
	}
	c.running = true
	lease, owner := c.lease, c.owner
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 3)
	start := func(name string, run func(context.Context) error) {
		go func() {
			err := run(ctx)
			if err == nil && ctx.Err() == nil {
				err = errors.New("stopped unexpectedly")
			}
			if err != nil {
				err = fmt.Errorf("wingd: local %s: %w", name, err)
			}
			results <- err
		}()
	}
	start("queue", c.queue.Run)
	start("activity", func(ctx context.Context) error { return c.State.Watch(ctx, report) })
	start("maintenance", func(ctx context.Context) error { return c.maintain(ctx, lease, owner) })
	var result error
	for range 3 {
		result = errors.Join(result, <-results)
		cancel()
	}
	return result
}

func (c *localController) maintain(ctx context.Context, lease, owner store.SingletonLease) error {
	tick := time.NewTicker(limits.Renew(limits.DefaultLease))
	defer tick.Stop()
	var nextMaintenance time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			var err error
			lease, err = c.operator.RenewSingletonLease(ctx, lease, limits.DefaultLease)
			if err != nil {
				return err
			}
			if owner, err = c.ownRuns(ctx, owner); err != nil {
				return err
			}
			if err := c.sweep(ctx, lease); err != nil {
				return err
			}
			if c.services.Complete != nil {
				if err := c.services.Complete(ctx); err != nil {
					return err
				}
			}
			if c.services.Maintain != nil && !time.Now().Before(nextMaintenance) {
				c.services.Maintain(ctx)
				nextMaintenance = time.Now().Add(controller.StorageMaintenanceInterval)
			}
		}
	}
}

func (c *localController) sweep(ctx context.Context, lease store.SingletonLease) error {
	const batch = 100
	for {
		units, err := c.operator.ReapUnits(ctx, lease, batch)
		if err != nil {
			return err
		}
		if len(units) < batch {
			break
		}
	}
	_, err := c.operator.MaintainConcurrency(ctx, lease, store.ConcurrencyMaintenanceOptions{})
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
