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

// LocalController supplies the owned store, workers and authorized HTTP handlers to the local process.
type LocalController interface {
	RunStore
	// TypeSafeAPIKey reads the optional Jev key through the controller's owned secrets store.
	TypeSafeAPIKey(context.Context) (string, error)
	Reconcile(context.Context) (LocalActivity, error)
	Run(context.Context, func(LocalActivity) error) error
	HTTP() LocalHTTP
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
	daemon    *Daemon
	cancel    context.CancelFunc
	done      chan struct{}
	ready     bool
	err       error
	dashboard chan localDashboardRequest
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
	d.localControl = func(stay *bool) LocalStatus { return r.control(session, stay) }
	d.localDashboard = r.EnsureDashboard
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
	r.run = &localRuntimeSession{daemon: d, cancel: cancel, done: make(chan struct{}), dashboard: make(chan localDashboardRequest)}
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
			return controller.HTTP().ServeAPI(ctx, &readyListener{Listener: ln, ready: apiReady})
		})
	}
	var dashboardListener net.Listener
	var dashboardAddress string
	var dashboardReady chan struct{}
	defer func() {
		if dashboardListener != nil {
			dashboardListener.Close()
		}
	}()
	startDashboard := func(bindCtx context.Context, address string) error {
		if dashboardListener != nil {
			if dashboardAddress != address {
				return fmt.Errorf("wingd: dashboard already serves %s; stop the controller before changing its address", dashboardAddress)
			}
			return nil
		}
		listen := r.listen
		if listen == nil {
			listen = func(network, address string) (net.Listener, error) {
				return (&net.ListenConfig{}).Listen(bindCtx, network, address)
			}
		}
		ln, err := listen("tcp", address)
		if err != nil {
			return fmt.Errorf("wingd: dashboard listen: %w", err)
		}
		dashboardListener, dashboardAddress = ln, address
		dashboardReady = make(chan struct{})
		workers.Go(func() {
			defer ln.Close()
			serve("dashboard", func() error {
				return controller.HTTP().ServeDashboard(ctx, &readyListener{Listener: ln, ready: dashboardReady})
			})
		})
		return nil
	}
	if dashboard != "" {
		if err := startDashboard(ctx, dashboard); err != nil {
			return err
		}
	} else {
		dashboardReady = make(chan struct{})
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
	var err error
running:
	for {
		select {
		case err = <-daemonDone:
			break running
		case request := <-session.dashboard:
			dashboardErr := request.ctx.Err()
			if dashboardErr == nil {
				dashboardErr = startDashboard(request.ctx, request.address)
			}
			if dashboardErr == nil {
				select {
				case <-dashboardReady:
				case <-ctx.Done():
					continue running
				case <-request.ctx.Done():
					dashboardErr = request.ctx.Err()
				}
			}
			if ctx.Err() != nil && request.ctx.Err() == nil {
				continue running
			}
			request.result <- dashboardErr
		}
	}
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
	return r.statusLocked(r.run), nil
}

func (r *localRuntime) statusLocked(session *localRuntimeSession) LocalStatus {
	d := session.daemon
	d.mu.Lock()
	defer d.mu.Unlock()
	activity := d.localActivity
	if !hasLocalActivity(activity) {
		activity.IdleSince = d.lastActivity
	}
	return LocalStatus{Running: session.ready && !d.shuttingDown && !d.draining, PID: os.Getpid(), Version: d.cfg.Version, Stay: d.stay, Activity: activity}
}

func (r *localRuntime) control(session *localRuntimeSession, stay *bool) LocalStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.statusLocked(session)
	if stay != nil && status.Running {
		if err := session.daemon.SetStay(*stay); err != nil {
			return r.statusLocked(session)
		}
		status = r.statusLocked(session)
	}
	return status
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
	if !r.run.ready {
		return errors.New("wingd: local controller is not ready")
	}
	return r.run.daemon.SetStay(stay)
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
