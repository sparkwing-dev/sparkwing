package main

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
	defaultWingdProbeInterval = 2 * time.Second
	defaultWingdProbeTimeout  = time.Second
	defaultWingdFailureLimit  = 3
	defaultWingdTermGrace     = 3 * time.Second
)

type wingdSupervisedChild interface {
	Wait() <-chan error
	Terminate() error
	Kill() error
}

type wingdSupervisorConfig struct {
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	FailureLimit  int
	TermGrace     time.Duration
}

type wingdSupervisorDeps struct {
	Start func() (wingdSupervisedChild, error)
	Probe func(context.Context) error
	Logf  func(string, ...any)
}

func (c wingdSupervisorConfig) validate() error {
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

// superviseWingd keeps recovery authority outside the serving runtime. A
// daemon that stops scheduling cannot run its own signal handler or watchdog,
// so only another process can bound recovery from that failure.
func superviseWingd(ctx context.Context, cfg wingdSupervisorConfig, deps wingdSupervisorDeps) error {
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
		recoverChild, err := watchWingdChild(ctx, child, cfg, deps)
		if err != nil {
			return err
		}
		if !recoverChild {
			return nil
		}
	}
}

func watchWingdChild(ctx context.Context, child wingdSupervisedChild, cfg wingdSupervisorConfig, deps wingdSupervisorDeps) (bool, error) {
	ticker := time.NewTicker(cfg.ProbeInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return false, stopWingdChild(child, cfg.TermGrace)
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
			if err := stopWingdChild(child, cfg.TermGrace); err != nil {
				return false, err
			}
			return true, nil
		}
	}
}

func stopWingdChild(child wingdSupervisedChild, grace time.Duration) error {
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

type execWingdChild struct {
	cmd  *exec.Cmd
	done chan error
}

func startExecWingdChild(self string, args []string) (wingdSupervisedChild, error) {
	cmd := exec.Command(self, args...)
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &execWingdChild{cmd: cmd, done: make(chan error, 1)}
	go func() { child.done <- cmd.Wait() }()
	return child, nil
}

func (c *execWingdChild) Wait() <-chan error { return c.done }
func (c *execWingdChild) Terminate() error   { return signalTerminate(c.cmd.Process.Pid) }
func (c *execWingdChild) Kill() error        { return signalKill(c.cmd.Process.Pid) }

func runWingdSupervise(args []string) error {
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
	return superviseWingd(ctx, wingdSupervisorConfig{
		ProbeInterval: defaultWingdProbeInterval,
		ProbeTimeout:  defaultWingdProbeTimeout,
		FailureLimit:  defaultWingdFailureLimit,
		TermGrace:     defaultWingdTermGrace,
	}, wingdSupervisorDeps{
		Start: func() (wingdSupervisedChild, error) {
			return startExecWingdChild(self, childArgs)
		},
		Probe: func(ctx context.Context) error {
			_, err := wingdclient.Query(ctx, wingdclient.Options{Home: *home, DialTimeout: defaultWingdProbeTimeout})
			return err
		},
		Logf: logger.Printf,
	})
}
