package wingd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

// LocalController supplies the shared controller implementation to the local process.
// Reconcile opens no second store and returns the initial activity after orphan recovery.
// Run owns the queue consumer and leased maintenance; it reports all four activity counts.
// ServeAPI consumes APIConn peer identity; ServeDashboard retains browser authentication.
// All three serving methods block until cancellation and join their children before returning.
// Close releases the single store handle after every service and admission finalizer joins.
type LocalController interface {
	RunStore
	// TypeSafeAPIKey reads the optional Jev key through the controller's owned secrets store.
	TypeSafeAPIKey(context.Context) (string, error)
	Reconcile(context.Context) (LocalActivity, error)
	Run(context.Context, func(LocalActivity) error) error
	ServeAPI(context.Context, net.Listener) error
	ServeDashboard(context.Context, net.Listener) error
	Close() error
}

// LocalRuntimeConfig binds an in-process controller to one home and admission policy.
// Open runs only under the home's election lock. It must clean up partial opens on error.
// Admission.Runs and Admission.ServeAPI are supplied by Open and must be unset.
type LocalRuntimeConfig struct {
	Admission Config
	Open      func(context.Context, string) (LocalController, error)
}

type localRuntime struct {
	cfg    LocalRuntimeConfig
	layout layout
	listen func(network, address string) (net.Listener, error)
	mu     sync.Mutex
	change chan struct{}
	run    *localRuntimeSession
}

type localRuntimeSession struct {
	daemon *Daemon
	cancel context.CancelFunc
	done   chan struct{}
	ready  bool
	err    error
}

// NewLocalRuntime constructs the elected local process used by LocalLifecycle.
// An empty DashboardAddress serves api.sock only; the CLI supplies its dashboard address.
// Another process's election is refused without opening its store.
func NewLocalRuntime(cfg LocalRuntimeConfig) (LocalRuntime, error) {
	if cfg.Open == nil || cfg.Admission.Runs != nil || cfg.Admission.ServeAPI != nil {
		return nil, errors.New("wingd: local runtime requires one elected controller factory")
	}
	lay, err := resolveLayout(cfg.Admission.Home)
	if err != nil {
		return nil, err
	}
	cfg.Admission.Home = lay.home
	return &localRuntime{cfg: cfg, layout: lay, change: make(chan struct{})}, nil
}

func (r *localRuntime) Run(ctx context.Context, opts LocalStartOptions) (err error) {
	home, err := HomeDir(opts.Home)
	if err != nil {
		return err
	}
	if home != r.layout.home {
		return errors.New("wingd: runtime is bound to another home")
	}
	if opts.Version == "" {
		return errors.New("wingd: local controller version is required")
	}
	cfg := r.cfg.Admission
	cfg.Version, cfg.Stay = opts.Version, opts.Stay
	d, err := New(cfg)
	if err != nil {
		return err
	}
	if r.listen != nil {
		d.listen = func(path string) (net.Listener, error) { return r.listen("unix", path) }
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	session, err := r.begin(d, cancel)
	if err != nil {
		return err
	}
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		session.err = err
		close(session.done)
		r.changed()
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	won, err := d.elect()
	if err != nil {
		return err
	}
	if !won {
		return ErrNotElected
	}
	defer d.releaseLock()
	controller, err := r.cfg.Open(ctx, home)
	if err != nil {
		return err
	}
	if controller == nil {
		return errors.New("wingd: controller factory returned no controller")
	}
	defer func() { err = errors.Join(err, controller.Close()) }()
	activity, err := controller.Reconcile(ctx)
	if err != nil {
		return fmt.Errorf("wingd: reconcile local runs: %w", err)
	}
	if err := controller.Ready(); err != nil {
		return err
	}
	if err := d.SetLocalActivity(activity); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.cfg.admissionPolicy().Mode == admission.ModeJev && d.jevAdvisor == nil {
		key, keyErr := controller.TypeSafeAPIKey(ctx)
		key = strings.TrimSpace(key)
		if keyErr == nil && key == "" {
			keyErr = errors.New("not configured")
		}
		if keyErr != nil {
			// safety: admission remains available through its existing fallback when Jev has no key.
			d.cfg.logf("jev admission needs the TYPESAFE_API_KEY secret (sparkwing secrets set --name TYPESAFE_API_KEY --file <path>): %v", keyErr)
		} else {
			d.jevAdvisor = newTypeSafeJevAdvisor(key, d.cfg.admissionPolicy().Jev)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.cfg.Runs = controller
	return r.serve(ctx, session, controller, opts.DashboardAddress)
}

func (r *localRuntime) begin(d *Daemon, cancel context.CancelFunc) (*localRuntimeSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run != nil {
		select {
		case <-r.run.done:
		default:
			return nil, errors.New("wingd: local runtime is already starting or running")
		}
	}
	r.run = &localRuntimeSession{daemon: d, cancel: cancel, done: make(chan struct{})}
	r.changed()
	return r.run, nil
}

func (r *localRuntime) changed() {
	close(r.change)
	r.change = make(chan struct{})
}

func (r *localRuntime) serve(ctx context.Context, session *localRuntimeSession, controller LocalController, dashboard string) error {
	d := session.daemon
	var workers sync.WaitGroup
	var failuresMu sync.Mutex
	var failures error
	stopping := func() bool {
		select {
		case <-d.quit:
			return true
		default:
			return ctx.Err() != nil
		}
	}
	serve := func(name string, run func() error) {
		err := run()
		stopped := stopping()
		if err == nil && !stopped {
			err = fmt.Errorf("wingd: %s stopped unexpectedly", name)
		}
		if err != nil && !(stopped && localShutdownError(err)) {
			failuresMu.Lock()
			failures = errors.Join(failures, fmt.Errorf("wingd: %s: %w", name, err))
			failuresMu.Unlock()
			session.cancel()
		}
	}
	apiReady := make(chan struct{})
	d.cfg.ServeAPI = func(_ context.Context, ln net.Listener) {
		serve("controller API", func() error {
			return controller.ServeAPI(ctx, &readyListener{Listener: ln, ready: apiReady})
		})
	}
	dashboardReady := make(chan struct{})
	if dashboard != "" {
		listen := r.listen
		if listen == nil {
			listen = net.Listen
		}
		ln, err := listen("tcp", dashboard)
		if err != nil {
			return fmt.Errorf("wingd: dashboard listen: %w", err)
		}
		defer ln.Close()
		workers.Go(func() {
			serve("dashboard", func() error {
				return controller.ServeDashboard(ctx, &readyListener{Listener: ln, ready: dashboardReady})
			})
		})
	} else {
		close(dashboardReady)
	}
	workers.Go(func() {
		serve("controller workers", func() error { return controller.Run(ctx, d.SetLocalActivity) })
	})
	workers.Go(func() {
		select {
		case <-d.quit:
			session.cancel()
		case <-ctx.Done():
		}
	})
	daemonDone := make(chan error, 1)
	go func() {
		daemonDone <- d.runElected(ctx)
		session.cancel()
	}()
	for _, ready := range []<-chan struct{}{d.Ready(), apiReady, dashboardReady} {
		select {
		case <-ready:
		case <-ctx.Done():
		}
	}
	r.mu.Lock()
	if ctx.Err() == nil {
		session.ready = true
		r.changed()
	}
	r.mu.Unlock()
	err := <-daemonDone
	session.cancel()
	workers.Wait()
	// safety: the admission timeout can report a slow drain, but cannot close its backing store.
	if d.apiDone != nil {
		<-d.apiDone
	}
	d.finalizers.Wait()
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		err = nil
	}
	failuresMu.Lock()
	defer failuresMu.Unlock()
	return errors.Join(err, failures)
}

func localShutdownError(err error) bool {
	if err == context.Canceled || err == net.ErrClosed {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, part := range joined.Unwrap() {
			if !localShutdownError(part) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return localShutdownError(cause)
	}
	return false
}

type readyListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (l *readyListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ready) })
	return l.Listener.Accept()
}

func (r *localRuntime) AwaitReady(ctx context.Context) (LocalStatus, error) {
	var session *localRuntimeSession
	for {
		if err := ctx.Err(); err != nil {
			return LocalStatus{}, err
		}
		r.mu.Lock()
		change := r.change
		if session == nil && r.run != nil {
			select {
			case <-r.run.done:
			default:
				session = r.run
			}
		}
		if session != nil {
			select {
			case <-session.done:
				err := session.err
				r.mu.Unlock()
				if err == nil {
					err = errors.New("wingd: local controller stopped")
				}
				return LocalStatus{}, err
			default:
			}
			if session.ready {
				r.mu.Unlock()
				return r.Status(ctx)
			}
		}
		r.mu.Unlock()
		select {
		case <-change:
		case <-ctx.Done():
			return LocalStatus{}, ctx.Err()
		}
	}
}

func (r *localRuntime) Status(ctx context.Context) (LocalStatus, error) {
	if err := ctx.Err(); err != nil {
		return LocalStatus{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run == nil {
		return LocalStatus{}, nil
	}
	select {
	case <-r.run.done:
		if errors.Is(r.run.err, ErrLocalStoreIncompatible) {
			return LocalStatus{}, r.run.err
		}
		return LocalStatus{}, nil
	default:
	}
	d := r.run.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	activity := d.localActivity
	if !hasLocalActivity(activity) {
		activity.IdleSince = d.lastActivity
	}
	return LocalStatus{Running: r.run.ready && !d.shuttingDown, PID: os.Getpid(), Version: d.cfg.Version, Stay: d.stay, Activity: activity}, nil
}

func (r *localRuntime) SetStay(ctx context.Context, stay bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run == nil {
		return errors.New("wingd: local controller is not running")
	}
	select {
	case <-r.run.done:
		return errors.New("wingd: local controller is not running")
	default:
	}
	r.run.daemon.SetStay(stay)
	return nil
}

func (r *localRuntime) Drain(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	session := r.run
	if session == nil {
		r.mu.Unlock()
		return nil
	}
	select {
	case <-session.done:
		r.mu.Unlock()
		return session.err
	default:
	}
	if session.ready {
		session.daemon.markDraining()
	}
	session.cancel()
	r.mu.Unlock()
	select {
	case <-session.done:
		return session.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
