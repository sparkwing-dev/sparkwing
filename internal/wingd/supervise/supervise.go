package supervise

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

const (
	defaultProbeInterval     = 2 * time.Second
	defaultProbeTimeout      = 3 * time.Second
	defaultFailureLimit      = 3
	defaultStartupTimeout    = 30 * time.Second
	defaultHeartbeatStale    = wingd.HeartbeatStaleWindow
	defaultFailureCeiling    = 5 * time.Minute
	defaultRestartBackoff    = time.Second
	defaultMaxRestartBackoff = 30 * time.Second
)

// DefaultTermGrace is how long the supervisor lets a daemon it stopped exit
// before killing it. It exceeds the daemon's own drain of in-flight run
// finalizes, described on
// [github.com/sparkwing-dev/sparkwing/internal/wingd.FinalizeDrainWindow].
const DefaultTermGrace = 15 * time.Second

type Child interface {
	Wait() <-chan error
	Terminate() error
	Kill() error
}

type Config struct {
	ProbeInterval  time.Duration
	ProbeTimeout   time.Duration
	FailureLimit   int
	TermGrace      time.Duration
	StartupTimeout time.Duration
	HeartbeatStale time.Duration
	FailureCeiling time.Duration
	// RestartBackoff doubles on each replacement up to MaxRestartBackoff, and resets once a child has stayed
	// healthy for MaxRestartBackoff.
	RestartBackoff    time.Duration
	MaxRestartBackoff time.Duration
}

type Deps struct {
	Start     func() (Child, error)
	Probe     func(context.Context) error
	Heartbeat func() (uint64, error)
	Logf      func(string, ...any)
}

func (c Config) heartbeatStale() time.Duration {
	if c.HeartbeatStale > 0 {
		return c.HeartbeatStale
	}
	return defaultHeartbeatStale
}

func (c Config) failureCeiling() time.Duration {
	if c.FailureCeiling > 0 {
		return c.FailureCeiling
	}
	return defaultFailureCeiling
}

func (c Config) validate() error {
	if c.ProbeInterval <= 0 {
		return fmt.Errorf("wingd supervisor: probe interval must be positive, got %s", c.ProbeInterval)
	}
	if c.ProbeTimeout <= 0 {
		return fmt.Errorf("wingd supervisor: probe timeout must be positive, got %s", c.ProbeTimeout)
	}
	if c.FailureLimit <= 0 {
		return fmt.Errorf("wingd supervisor: failure limit must be positive, got %d", c.FailureLimit)
	}
	if c.TermGrace <= 0 {
		return fmt.Errorf("wingd supervisor: termination grace must be positive, got %s", c.TermGrace)
	}
	if c.StartupTimeout <= 0 {
		return fmt.Errorf("wingd supervisor: startup timeout must be positive, got %s", c.StartupTimeout)
	}
	if c.RestartBackoff <= 0 || c.MaxRestartBackoff < c.RestartBackoff {
		return fmt.Errorf("wingd supervisor: restart backoff must be positive and at most its maximum, got %s and %s",
			c.RestartBackoff, c.MaxRestartBackoff)
	}
	return nil
}

func Loop(ctx context.Context, cfg Config, deps Deps) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if deps.Start == nil || deps.Probe == nil {
		return errors.New("wingd supervisor: start and probe dependencies are required")
	}
	var backoff time.Duration
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		child, err := deps.Start()
		if err != nil {
			return fmt.Errorf("wingd supervisor: start child: %w", err)
		}
		recoverChild, resetBackoff, err := watchChild(ctx, child, cfg, deps)
		if err != nil {
			return err
		}
		if !recoverChild {
			return nil
		}
		if resetBackoff {
			backoff = 0
		}
		backoff = min(max(2*backoff, cfg.RestartBackoff), cfg.MaxRestartBackoff)
		if deps.Logf != nil {
			deps.Logf("starting a replacement daemon in %s", backoff)
		}
		if !sleepContext(ctx, backoff) {
			return nil
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func watchChild(ctx context.Context, child Child, cfg Config, deps Deps) (replace, resetBackoff bool, err error) {
	ticker := time.NewTicker(cfg.ProbeInterval)
	defer ticker.Stop()
	started := time.Now()
	var ready bool
	var healthySince time.Time
	var failureSince, lastProgress, lastWarning, lastTick time.Time
	var lastHeartbeat uint64
	var haveHeartbeat bool
	var staleSamples int
	failures := 0
	for {

		select {
		case err := <-child.Wait():
			return false, false, err
		default:
		}
		select {
		case <-ctx.Done():
			return false, false, stopChild(child, cfg.TermGrace)
		case err := <-child.Wait():
			return false, false, err
		case <-ticker.C:
			tick := time.Now()
			stallThreshold := 2 * (cfg.ProbeInterval + cfg.ProbeTimeout)
			stalled := !lastTick.IsZero() && tick.Sub(lastTick) > stallThreshold
			lastTick = tick
			probeCtx, cancel := context.WithTimeout(ctx, cfg.ProbeTimeout)
			err := deps.Probe(probeCtx)
			cancel()
			if time.Since(tick) > stallThreshold {
				stalled = true
			}
			if stalled {
				// safety: a stopped supervisor cannot judge progress made during the same machine stall.
				lastProgress = time.Now()
				staleSamples = 0
			}
			if err == nil {
				lastProgress = time.Now()
				ready = true
				if healthySince.IsZero() {
					healthySince = time.Now()
				}
				if time.Since(healthySince) >= cfg.MaxRestartBackoff {
					resetBackoff = true
				}
				failures = 0
				failureSince = time.Time{}
				staleSamples = 0
				lastWarning = time.Time{}
				continue
			}
			healthySince = time.Time{}
			if failureSince.IsZero() {
				failureSince = time.Now()
			}
			if deps.Heartbeat != nil {
				if counter, herr := deps.Heartbeat(); herr == nil {
					if haveHeartbeat && counter != lastHeartbeat {
						lastProgress = time.Now()
						staleSamples = 0
					}
					lastHeartbeat = counter
					haveHeartbeat = true
				}
			}
			staleSince := started
			if !lastProgress.IsZero() {
				staleSince = lastProgress
			}
			if time.Since(staleSince) >= cfg.heartbeatStale() {
				staleSamples++
			} else {
				staleSamples = 0
			}
			stale := staleSamples >= cfg.FailureLimit
			ceiling := time.Since(failureSince) >= cfg.failureCeiling()
			if !ready {
				if !ceiling && (time.Since(started) < cfg.StartupTimeout || !stale) {
					if !stale && deps.Logf != nil && (lastWarning.IsZero() || time.Since(lastWarning) >= cfg.heartbeatStale()) {
						deps.Logf("startup probe failed but daemon heartbeat advances: %v", err)
						lastWarning = time.Now()
					}
					continue
				}
				if deps.Logf != nil {
					if ceiling && !stale {
						deps.Logf("replacing daemon during startup: HARD CEILING of %s continuous probe failure reached despite advancing heartbeat (last probe: %v)", cfg.failureCeiling(), err)
					} else {
						deps.Logf("replacing daemon during startup: no successful probe for %s and heartbeat stale for %s (last probe: %v)", time.Since(started), time.Since(staleSince), err)
					}
				}
				return true, false, stopChild(child, cfg.TermGrace)
			}
			failures++
			if failures < cfg.FailureLimit || (!stale && !ceiling) {
				if deps.Logf != nil && (lastWarning.IsZero() || time.Since(lastWarning) >= cfg.heartbeatStale()) {
					deps.Logf("health probes failing (%d consecutive), but daemon heartbeat is %s old; keeping daemon (last probe: %v)", failures, time.Since(staleSince), err)
					lastWarning = time.Now()
				}
				continue
			}
			if deps.Logf != nil {
				if ceiling && !stale {
					deps.Logf("replacing daemon: HARD CEILING of %s continuous probe failure reached despite advancing heartbeat (last probe: %v)", cfg.failureCeiling(), err)
				} else {
					deps.Logf("replacing daemon: %d failed probes and heartbeat stale for %s (last probe: %v)", failures, time.Since(staleSince), err)
				}
			}
			if err := stopChild(child, cfg.TermGrace); err != nil {
				return false, false, err
			}
			return true, resetBackoff, nil
		}
	}
}

func stopChild(child Child, grace time.Duration) error {
	done := child.Wait()
	select {
	case waitErr := <-done:
		return waitErr
	default:
	}
	if err := child.Terminate(); err != nil {
		select {
		case waitErr := <-done:
			return waitErr
		default:
			if forceErr := killAndWaitChild(child, done, grace); forceErr != nil {
				return fmt.Errorf("wingd supervisor: terminate child: %v; forced stop: %w", err, forceErr)
			}
			return nil
		}
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	}
	if err := killAndWaitChild(child, done, grace); err != nil {
		return fmt.Errorf("wingd supervisor: %w", err)
	}
	return nil
}

func killAndWaitChild(child Child, done <-chan error, grace time.Duration) error {
	if err := child.Kill(); err != nil {
		return fmt.Errorf("kill child after %s: %w", grace, err)
	}
	killTimer := time.NewTimer(grace)
	defer killTimer.Stop()
	select {
	case <-done:
		return nil
	case <-killTimer.C:
		return fmt.Errorf("child did not exit after kill within %s", grace)
	}
}

type execChild struct {
	cmd  *exec.Cmd
	done chan error
}

func startExecChild(self string, args []string) (Child, error) {
	cmd := exec.Command(self, args...)
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &execChild{cmd: cmd, done: make(chan error, 1)}
	go func() { child.done <- cmd.Wait() }()
	return child, nil
}

func (c *execChild) Wait() <-chan error { return c.done }

func (c *execChild) Terminate() error {
	return signalExited(signalTerminate(c.cmd.Process))
}

func (c *execChild) Kill() error {
	return signalExited(signalKill(c.cmd.Process))
}

// safety: signalling the handle rather than the pid means a child the kernel
// has already reaped answers ErrProcessDone instead of letting the signal reach
// whichever process inherited its pid.
func signalExited(err error) error {
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func Run(args []string) error {
	fs := flag.NewFlagSet("wingd supervise", flag.ContinueOnError)
	home := fs.String("home", "", "")
	version := fs.String("version", "", "")
	admissionConfig := fs.String("admission-config", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("wingd supervise: unexpected positional arguments")
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}
	childArgs := []string{"wingd", "run"}
	if *home != "" {
		childArgs = append(childArgs, "--home", *home)
	}
	if *version != "" {
		childArgs = append(childArgs, "--version", *version)
	}
	if *admissionConfig != "" {
		childArgs = append(childArgs, "--admission-config", *admissionConfig)
	}
	logger := log.New(os.Stderr, "wingd supervisor: ", log.LstdFlags|log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Loop(ctx, Config{
		ProbeInterval:     defaultProbeInterval,
		ProbeTimeout:      defaultProbeTimeout,
		FailureLimit:      defaultFailureLimit,
		TermGrace:         DefaultTermGrace,
		StartupTimeout:    defaultStartupTimeout,
		RestartBackoff:    defaultRestartBackoff,
		MaxRestartBackoff: defaultMaxRestartBackoff,
	}, Deps{
		Start: func() (Child, error) {
			return startExecChild(self, childArgs)
		},

		Probe: func(ctx context.Context) error {
			return wingdclient.HealthProbe(ctx, *home)
		},
		Heartbeat: func() (uint64, error) {
			path, err := wingd.HeartbeatPath(*home)
			if err != nil {
				return 0, err
			}
			return wingd.ReadHeartbeat(path)
		},
		Logf: logger.Printf,
	})
}
