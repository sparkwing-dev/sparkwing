package localcontroller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// State supplies the local recovery and activity projection over the owned store.
type State interface {
	Ready() error
	Reconcile(context.Context) (wingd.LocalActivity, error)
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
	HTTP     localhostwire.LocalHTTP
	// Runner supervises the local runner and reports each check to its callback.
	Runner   func(context.Context, func(error)) error
	Close    func() error
	Maintain func(context.Context)
	Complete func(context.Context) error
	// Deliver sends run event deliveries until its context ends. It runs in
	// its own worker so a slow receiver never stalls maintenance.
	Deliver func(context.Context) error
}

// Config supplies shared service constructors without opening another store.
type Config struct {
	Bind func(context.Context, Binding) (Services, error)
	// StoreOptions configures the owned store before service binding.
	StoreOptions []store.Option
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
	epoch, err := credentials.LoadOrCreateEpoch(home)
	if err != nil {
		return nil, err
	}
	var services Services
	var binding Binding
	options := append([]store.Option(nil), cfg.StoreOptions...)
	options = append(options, store.WithCredentialEpoch(epoch), store.WithPolicies(func(s *store.Store) (store.Policies, error) {
		tenant, err := s.ForTeam(ctx, store.DefaultTeam)
		if err != nil {
			return store.Policies{}, err
		}
		binding = Binding{Home: home, Store: s, Tenant: tenant, Controller: controller.NewEngineController(s, controller.EngineController{})}
		services, err = cfg.Bind(ctx, binding)
		return services.Policies, err
	}))
	s, err := store.Open(filepath.Join(home, "state.db"), options...) //nolint:contextcheck // safety: Store.Open has no context parameter; election owns initialization.
	if err != nil {
		return nil, errors.Join(err, closeServices(services))
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeServices(services), s.Close())
		}
	}()

	if err := s.EnsureCredentialEpoch(ctx); err != nil {
		if errors.Is(err, store.ErrCredentialEpochChanged) {
			err = fmt.Errorf("%w: %s is missing or replaced (a missing file is created afresh); put the original file back, or move state.db aside to start with a fresh local store", err, filepath.Join(home, "credential-epoch"))
		}
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if services.State == nil || services.HTTP.API == nil || services.HTTP.Dashboard == nil || services.Runner == nil || binding.Controller.RunnerPolicy == nil || binding.Controller.ResolveTenant == nil {
		return nil, errors.New("wingd: local services require state, runner, signed policy and shared HTTP handlers")
	}
	return &localController{State: services.State, services: services, store: s, operator: s.AsOperator(), tenant: binding.Tenant}, nil
}

type localController struct {
	State
	services Services
	store    *store.Store
	operator *store.Operator
	tenant   *store.Tenant
	mu       sync.Mutex
	// reconciled is set once startup recovery has run; workers and admission cancellation need it.
	reconciled bool
	running    bool
	closed     bool
	closeErr   error
}

// LocalIdleActivity reads fresh work counts before the controller retires.
func (c *localController) LocalIdleActivity(ctx context.Context) (wingd.LocalActivity, error) {
	return snapshotLocalActivity(ctx, c.tenant)
}

func (c *localController) HTTP() localhostwire.LocalHTTP { return c.services.HTTP }

func (c *localController) Reconcile(ctx context.Context) (wingd.LocalActivity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.running || c.reconciled {
		return wingd.LocalActivity{}, errors.New("wingd: local reconciliation requires a new controller")
	}
	if err := c.sweep(ctx); err != nil {
		return wingd.LocalActivity{}, err
	}
	activity, err := c.State.Reconcile(ctx)
	if err == nil {
		err = c.ownRuns(ctx)
	}
	if err == nil {
		c.reconciled = true
	}
	return activity, err
}

// ownRuns schedules and recovers controller-dispatched runs; the SQLite store has only this process as its controller.
func (c *localController) ownRuns(ctx context.Context) error {
	for after := ""; ; {
		page, err := c.operator.ReconcileControllerRuns(ctx, after, 500)
		if err != nil || len(page) < 500 {
			return err
		}
		after = page[len(page)-1].RunID
	}
}

func (c *localController) Run(ctx context.Context, reports wingd.LocalReports) error {
	c.mu.Lock()
	if c.closed || c.running || !c.reconciled || reports.Activity == nil || reports.Runner == nil {
		c.mu.Unlock()
		return errors.New("wingd: local workers require reconciled state and activity and runner reporters")
	}
	c.running = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workers := 3
	if c.services.Deliver != nil {
		workers++
	}
	results := make(chan error, workers)
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
	start("queue", func(ctx context.Context) error { return c.services.Runner(ctx, reports.Runner) })
	start("activity", func(ctx context.Context) error { return c.State.Watch(ctx, reports.Activity) })
	start("maintenance", func(ctx context.Context) error { return c.maintain(ctx) })
	if c.services.Deliver != nil {
		start("run event delivery", c.services.Deliver)
	}
	var result error
	for range workers {
		result = errors.Join(result, <-results)
		cancel()
	}
	return result
}

func (c *localController) maintain(ctx context.Context) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var nextMaintenance time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := c.ownRuns(ctx); err != nil {
				return err
			}
			if err := c.sweep(ctx); err != nil {
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

func (c *localController) sweep(ctx context.Context) error {
	const batch = 100
	for {
		units, err := c.operator.ReapUnits(ctx, batch)
		if err != nil {
			return err
		}
		if len(units) < batch {
			break
		}
	}
	_, err := c.operator.MaintainConcurrency(ctx)
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
