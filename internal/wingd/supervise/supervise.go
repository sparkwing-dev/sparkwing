// Package supervise runs the external watchdog for the local admission
// daemon: `<binary> wingd supervise` starts `<binary> wingd run` as a
// child, probes it, and replaces it when it stops answering.
//
// It lives outside cmd/sparkwing because the spawn path re-execs whichever
// binary hosts the daemon, and more than one installed binary does: the
// sparkwing CLI and sparkwing-runner both serve `wingd supervise` from
// here, so a daemon spawned by either gets the same recovery behavior
// rather than one of them answering "usage: wingd run".
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
	defaultProbeInterval = 2 * time.Second
	defaultProbeTimeout  = time.Second
	defaultFailureLimit  = 3
	defaultTermGrace     = 3 * time.Second
)

// Child is the supervised daemon process, as the loop needs to see it.
type Child interface {
	Wait() <-chan error
	Terminate() error
	Kill() error
}

// Config tunes the watchdog: how often to probe, how long a probe may
// take, how many consecutive failures condemn the child, and how long a
// condemned child has to exit on its own before it is killed.
type Config struct {
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	FailureLimit  int
	TermGrace     time.Duration
}

// Deps are the injectable edges of the loop, so tests drive it without
// starting processes or dialing sockets.
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

// Loop keeps recovery authority outside the serving runtime. A daemon that
// stops scheduling cannot run its own signal handler or watchdog, so only
// another process can bound recovery from that failure.
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
	if err := child.Terminate(); err != nil {
		select {
		case waitErr := <-done:
			return waitErr
		default:
			return fmt.Errorf("wingd supervisor: terminate child: %w", err)
		}
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	}
	if err := child.Kill(); err != nil {
		return fmt.Errorf("wingd supervisor: kill child after %s: %w", grace, err)
	}
	<-done
	return nil
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
func (c *execChild) Terminate() error   { return signalTerminate(c.cmd.Process.Pid) }
func (c *execChild) Kill() error        { return signalKill(c.cmd.Process.Pid) }

// Run serves `<binary> wingd supervise [--home DIR] [--version V]` for
// any binary that hosts the daemon. It re-execs itself as `wingd run`
// with the same flags, so the serving daemon is always the same build as
// the supervisor that owns its recovery.
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
		TermGrace:     defaultTermGrace,
	}, Deps{
		Start: func() (Child, error) {
			return startExecChild(self, childArgs)
		},
		// The probe must ride [wingdclient.HealthProbe], never a working
		// client: a working connection counts as daemon activity, and a
		// daemon its own watchdog keeps active can never idle out, so the
		// supervise+run pair outlives every home that spawned it.
		Probe: func(ctx context.Context) error {
			return wingdclient.HealthProbe(ctx, *home)
		},
		Logf: logger.Printf,
	})
}
