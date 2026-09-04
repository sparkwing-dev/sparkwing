package supervise

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

const (
	defaultProbeInterval = 2 * time.Second
	defaultProbeTimeout  = time.Second
	defaultFailureLimit  = 3
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
	return nil
}

func Loop(ctx context.Context, cfg Config, deps Deps) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if deps.Start == nil || deps.Probe == nil {
		return errors.New("wingd supervisor: start and probe dependencies are required")
	}
	for {
		child, err := deps.Start()
		if err != nil {
			return fmt.Errorf("wingd supervisor: start child: %w", err)
		}
		recoverChild, err := watchChild(ctx, child, cfg, deps)
		if err != nil {
			return err
		}
		if !recoverChild {
			return nil
		}
	}
}

func watchChild(ctx context.Context, child Child, cfg Config, deps Deps) (bool, error) {
	ticker := time.NewTicker(cfg.ProbeInterval)
	defer ticker.Stop()
	failures := 0
	for {

		select {
		case err := <-child.Wait():
			return false, err
		default:
		}
		select {
		case <-ctx.Done():
			return false, stopChild(child, cfg.TermGrace)
		case err := <-child.Wait():
			return false, err
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, cfg.ProbeTimeout)
			err := deps.Probe(probeCtx)
			cancel()
			if err == nil {
				failures = 0
				continue
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
				return false, err
			}
			return true, nil
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
	cmd    *exec.Cmd
	done   chan error
	reaped atomic.Bool
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
	go func() {
		err := cmd.Wait()
		// safety: the kernel can hand this pid to an unrelated process the moment
		// Wait returns, so mark it unsignallable before announcing the exit.
		child.reaped.Store(true)
		child.done <- err
	}()
	return child, nil
}

func (c *execChild) Wait() <-chan error { return c.done }

func (c *execChild) Terminate() error {
	if c.reaped.Load() {
		return nil
	}
	return signalTerminate(c.cmd.Process.Pid)
}

func (c *execChild) Kill() error {
	if c.reaped.Load() {
		return nil
	}
	return signalKill(c.cmd.Process.Pid)
}

func Run(args []string) error {
	fs := flag.NewFlagSet("wingd supervise", flag.ContinueOnError)
	home := fs.String("home", "", "")
	version := fs.String("version", "", "")
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
	logger := log.New(os.Stderr, "wingd supervisor: ", log.LstdFlags|log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Loop(ctx, Config{
		ProbeInterval: defaultProbeInterval,
		ProbeTimeout:  defaultProbeTimeout,
		FailureLimit:  defaultFailureLimit,
		TermGrace:     DefaultTermGrace,
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
