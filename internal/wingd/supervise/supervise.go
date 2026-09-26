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

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

const (
	defaultProbeInterval     = 2 * time.Second
	defaultProbeTimeout      = time.Second
	defaultFailureLimit      = 3
	defaultStartupTimeout    = 30 * time.Second
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
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	FailureLimit  int
	TermGrace     time.Duration
	// StartupTimeout bounds how long a child may go without answering its first probe.
	StartupTimeout time.Duration
	// RestartBackoff doubles on each replacement up to MaxRestartBackoff, and resets once a child has stayed
	// healthy for MaxRestartBackoff.
	RestartBackoff    time.Duration
	MaxRestartBackoff time.Duration
}

type Deps struct {
	Start func() (Child, error)
	Probe func(context.Context) error
	Logf  func(string, ...any)
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
		if ctx.Err() != nil {
			return nil
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
		if err := sleepContext(ctx, backoff); err != nil {
			return nil
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func watchChild(ctx context.Context, child Child, cfg Config, deps Deps) (replace, resetBackoff bool, err error) {
	ticker := time.NewTicker(cfg.ProbeInterval)
	defer ticker.Stop()
	started := time.Now()
	var ready bool
	var healthySince time.Time
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
			probeCtx, cancel := context.WithTimeout(ctx, cfg.ProbeTimeout)
			err := deps.Probe(probeCtx)
			cancel()
			if err == nil {
				ready = true
				if healthySince.IsZero() {
					healthySince = time.Now()
				}
				if time.Since(healthySince) >= cfg.MaxRestartBackoff {
					resetBackoff = true
				}
				failures = 0
				continue
			}
			healthySince = time.Time{}
			if !ready {
				if time.Since(started) < cfg.StartupTimeout {
					continue
				}
				if deps.Logf != nil {
					deps.Logf("daemon did not answer a probe within %s of starting; replacing it", cfg.StartupTimeout)
				}
				return true, false, stopChild(child, cfg.TermGrace)
			}
			failures++
			if deps.Logf != nil {
				deps.Logf("health probe %d/%d failed: %v", failures, cfg.FailureLimit, err)
			}
			if failures < cfg.FailureLimit {
				continue
			}
			if deps.Logf != nil {
				deps.Logf("health probe failed %d times; replacing unresponsive daemon", failures)
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
		Logf: logger.Printf,
	})
}
