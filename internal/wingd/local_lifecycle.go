package wingd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
)

var ErrLocalStoreIncompatible = errors.New("wingd: incompatible local store")

type localRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

type localLifecycle struct {
	runtime LocalRuntime
	units   ManagedUnitManager
	gate    chan struct{}
	run     *localRun
	home    string
}

func (l *localLifecycle) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *localLifecycle) EnsureStarted(ctx context.Context, opts LocalStartOptions) (LocalStatus, error) {
	if err := l.lock(ctx); err != nil {
		return LocalStatus{}, err
	}
	defer func() { <-l.gate }()
	if l.runtime == nil {
		return LocalStatus{}, errors.New("wingd: local controller runtime is required")
	}
	home, err := HomeDir(opts.Home)
	if err != nil {
		return LocalStatus{}, err
	}
	opts.Home = home
	if l.home != "" && filepath.Clean(opts.Home) != l.home {
		return LocalStatus{}, errors.New("wingd: lifecycle is already bound to another home")
	}
	l.home = filepath.Clean(opts.Home)
	status, err := l.runtime.Status(ctx)
	if err != nil {
		return LocalStatus{}, err
	}
	if status.Running {
		return l.reuse(ctx, opts, status)
	}
	if l.run != nil {
		select {
		case <-l.run.done:
		default:
			return LocalStatus{}, errors.New("wingd: previous controller has not finished shutting down")
		}
	}
	// safety: an on-demand request ending must not stop a controller hosting other runs.
	parent := context.Background()
	if opts.Standalone {
		parent = ctx
	}
	runCtx, cancel := context.WithCancel(parent)
	run := &localRun{cancel: cancel, done: make(chan struct{})}
	l.run = run
	go func() {
		run.err = l.runtime.Run(runCtx, opts)
		close(run.done)
	}()
	if err := l.awaitReady(ctx, run); err != nil {
		cancel()
		<-run.done
		if errors.Is(err, ErrNotElected) && run.err == ErrNotElected {
			status, queryErr := l.runtime.Status(ctx)
			if queryErr != nil {
				return LocalStatus{}, errors.Join(err, queryErr)
			}
			if status.Running {
				return l.reuse(ctx, opts, status)
			}
		}
		return LocalStatus{}, err
	}
	status, err = l.Status(ctx)
	if err == nil && !status.Running {
		err = errors.New("wingd: controller stopped during startup")
	}
	return status, err
}

func (l *localLifecycle) reuse(ctx context.Context, opts LocalStartOptions, status LocalStatus) (LocalStatus, error) {
	if opts.Standalone {
		return LocalStatus{}, errors.New("wingd: standalone cannot open a store owned by a running controller")
	}
	if opts.Stay && !status.Stay {
		if err := l.runtime.SetStay(ctx, true); err != nil {
			return LocalStatus{}, err
		}
	}
	status, err := l.Status(ctx)
	if err == nil && !status.Running {
		err = errors.New("wingd: controller stopped during startup")
	}
	return status, err
}

func (l *localLifecycle) awaitReady(ctx context.Context, run *localRun) error {
	ctx, cancel := context.WithCancel(ctx)
	ready := make(chan error, 1)
	finished := make(chan struct{})
	defer func() {
		cancel()
		<-finished
	}()
	go func() {
		defer close(finished)
		status, err := l.runtime.AwaitReady(ctx)
		if err == nil && !status.Running {
			err = errors.New("wingd: runtime reported readiness without a running controller")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		return err
	case <-run.done:
		if run.err != nil {
			return run.err
		}
		return errors.New("wingd: controller exited before becoming ready")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *localLifecycle) StartDashboard(ctx context.Context, opts LocalStartOptions) (LocalStatus, error) {
	status, err := l.EnsureStarted(ctx, opts)
	if err != nil || opts.DashboardAddress == "" {
		return status, err
	}
	if err := l.runtime.EnsureDashboard(ctx, opts.DashboardAddress); err != nil {
		return LocalStatus{}, err
	}
	status, err = l.Status(ctx)
	if err == nil && !status.Running {
		err = errors.New("wingd: controller stopped during dashboard startup")
	}
	return status, err
}

func (l *localLifecycle) Stop(ctx context.Context) error {
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer func() { <-l.gate }()
	return l.stop(ctx)
}

func (l *localLifecycle) stop(ctx context.Context) error {
	if l.runtime == nil {
		return errors.New("wingd: local controller runtime is required")
	}
	if err := l.runtime.Drain(ctx); err != nil {
		return err
	}
	if l.run == nil {
		return nil
	}
	select {
	case <-l.run.done:
		l.run.cancel()
		if errors.Is(l.run.err, ErrNotElected) {
			return nil
		}
		return l.run.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *localLifecycle) InstallController(ctx context.Context, opts LocalStartOptions) (LocalStatus, error) {
	if err := l.lock(ctx); err != nil {
		return LocalStatus{}, err
	}
	defer func() { <-l.gate }()
	if l.runtime == nil || l.units == nil {
		return LocalStatus{}, errors.New("wingd: service installation requires the local runtime and managed units")
	}
	home, err := HomeDir(opts.Home)
	if err != nil {
		return LocalStatus{}, err
	}
	if l.home != "" && home != l.home || !filepath.IsAbs(opts.Executable) || opts.Version == "" || opts.Standalone || opts.DashboardAddress != "" {
		return LocalStatus{}, errors.New("wingd: service installation requires the bound home, executable and version")
	}
	l.home, opts.Home, opts.Stay = home, home, true
	previous, err := l.runtime.Status(ctx)
	if err != nil {
		return LocalStatus{}, err
	}
	if err := l.stop(ctx); err != nil {
		return LocalStatus{}, fmt.Errorf("wingd: drain previous controller before service installation: %w", err)
	}
	if err := l.units.InstallController(ctx, opts); err != nil {
		return LocalStatus{}, fmt.Errorf("wingd: service installation failed after draining the previous controller: %w", err)
	}
	ready, err := l.runtime.AwaitReady(ctx)
	if err != nil {
		return LocalStatus{}, fmt.Errorf("wingd: service installed but its successor did not become ready: %w", err)
	}
	status, err := l.Status(ctx)
	if err != nil {
		return LocalStatus{}, fmt.Errorf("wingd: verify installed controller: %w", err)
	}
	if !status.Running || !status.Stay || !status.Installed || status.Version != opts.Version || status.PID <= 0 || status.PID != ready.PID || status.PID == previous.PID {
		return LocalStatus{}, errors.New("wingd: service installed but a new ready controller with the installed version and stay mode was not verified")
	}
	return status, nil
}

func (l *localLifecycle) Status(ctx context.Context) (LocalStatus, error) {
	if l.runtime == nil {
		return LocalStatus{}, errors.New("wingd: local controller runtime is required")
	}
	status, err := l.runtime.Status(ctx)
	if err != nil || l.units == nil {
		return status, err
	}
	units, err := l.units.Inspect(ctx)
	if err != nil {
		return LocalStatus{}, err
	}
	for _, unit := range units {
		if unit.Kind == ManagedController {
			status.Installed = true
		}
	}
	return status, nil
}

func (l *localLifecycle) Doctor(ctx context.Context, version string) ([]LocalDoctorFinding, error) {
	status, err := l.Status(ctx)
	if errors.Is(err, ErrLocalStoreIncompatible) {
		return []LocalDoctorFinding{{Code: "incompatible_store", Message: err.Error(), Hint: "Use the controller version that created this store, or select a fresh home."}}, nil
	}
	if err != nil {
		return nil, err
	}
	var findings []LocalDoctorFinding
	if status.Running && status.Version != version {
		findings = append(findings, LocalDoctorFinding{Code: "version_skew", Message: fmt.Sprintf("controller version %q differs from CLI %q", status.Version, version), Hint: "Restart the local controller with sparkwing dashboard stop and sparkwing dashboard start."})
	}
	if l.units == nil {
		return findings, nil
	}
	units, err := l.units.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	for _, unit := range units {
		if unit.Version != version {
			findings = append(findings, LocalDoctorFinding{Code: "unit_version_skew", Unit: unit.Name, Message: fmt.Sprintf("managed unit version %q differs from CLI %q", unit.Version, version), Hint: "Run sparkwing update to refresh managed units."})
		}
		if !validUnitArguments(unit) {
			findings = append(findings, LocalDoctorFinding{Code: "removed_unit_verb", Unit: unit.Name, Message: fmt.Sprintf("managed unit has unsupported arguments %q", unit.Arguments), Hint: "Reinstall this unit with sparkwing dashboard install or sparkwing schedules install."})
		}
	}
	return findings, nil
}

func validUnitArguments(unit ManagedUnit) bool {
	if !filepath.IsAbs(unit.Home) {
		return false
	}
	switch unit.Kind {
	case ManagedController:
		return slices.Equal(unit.Arguments, []string{"wingd", "run", "--stay", "--home", unit.Home})
	case ManagedSchedule:
		return slices.Equal(unit.Arguments, []string{"__tick", "--home", unit.Home})
	default:
		return false
	}
}
