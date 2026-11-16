package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const (
	localRunnerCheckInterval = time.Second
	localRunnerMaxRetry      = time.Minute
)

var (
	startLocalRunnerProcess = wingd.StartLocalRunnerProcess
	waitLocalRunnerShutdown = wingd.WaitLocalRunnerShutdown
)

type localRunnerDiscovery interface {
	Active(context.Context) (localhostwire.LocalRunnerInstance, error)
	Read(context.Context, string) (localhostwire.LocalRunnerInstance, error)
}

type localRunnerProcessSupervisor struct {
	registry localRunnerDiscovery
	version  string
	build    string
	start    func(context.Context, string) error
	alive    func(localhostwire.LocalRunnerInstance) (bool, error)
	report   func(error)
	limits   wingd.RunnerLimits
	applied  func(localhostwire.LocalRunnerInstance) (wingd.RunnerLimits, bool)
	// safety: runners this supervisor started keep whatever limits the service manager granted,
	// so a refused limit cannot restart them on every check.
	started map[string]bool
}

func superviseLocalRunnerProcess(ctx context.Context, host crontimer.Host, report func(error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	executable, build, err := engine.RetainExecutable(filepath.Join(host.SparkwingHome, "runners", "builds"), host.Binary, "")
	if err != nil {
		return err
	}
	host.Binary = executable
	host.Env = make(map[string]string)
	for _, assignment := range os.Environ() {
		name, value, ok := strings.Cut(assignment, "=")
		if ok && name != "" {
			host.Env[name] = value
		}
	}
	registry, err := wingdclient.NewLocalRunnerClient(host.SparkwingHome, host.Version)
	if err != nil {
		return err
	}
	limits := localRunnerLimits()
	supervisor := localRunnerProcessSupervisor{
		registry: registry, version: host.Version, build: build, alive: wingd.LocalRunnerProcessAlive, report: report,
		start: func(ctx context.Context, id string) error {
			return startLocalRunnerProcess(ctx, host, id, limits)
		},
		limits: limits, started: make(map[string]bool),
		applied: func(instance localhostwire.LocalRunnerInstance) (wingd.RunnerLimits, bool) {
			return wingd.LocalRunnerAppliedLimits(instance.PID)
		},
	}
	return supervisor.run(ctx)
}

func (s localRunnerProcessSupervisor) run(ctx context.Context) error {
	delay := localRunnerCheckInterval
	for {
		// safety: runs hosted by the CLI need no runner, so a runner that cannot start must not stop
		// the controller; queued jobs wait and the controller log names the cause.
		serving, err := s.ensure(ctx)
		if ctx.Err() == nil {
			if serving {
				s.report(nil)
			} else {
				s.report(err)
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("local runner unavailable; queued jobs wait until it starts", "err", err, "retry_in", delay)
			delay = min(2*delay, localRunnerMaxRetry)
		} else {
			delay = localRunnerCheckInterval
		}
		select {
		case <-ctx.Done():
			// safety: controller replacement must leave runner-owned hosts and services alive.
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// safety: serving counts an outdated runner still active while its successor fails to start; its jobs still run.
func (s localRunnerProcessSupervisor) ensure(ctx context.Context) (serving bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	instance, err := s.registry.Active(ctx)
	if err == nil {
		alive, err := s.alive(instance)
		if err != nil {
			return false, fmt.Errorf("inspect local runner %s: %w", instance.ID, err)
		}
		if alive && instance.Version == s.version && instance.BuildIdentity == s.build && s.limitsCurrent(instance) {
			return true, nil
		}
		serving = alive
	} else if !localRunnerNotFound(err) {
		return false, err
	}
	id := rand.Text()
	if s.started != nil {
		s.started[id] = true
	}
	ctx, cancel := context.WithTimeout(ctx, wingdclient.RequestTimeout)
	defer cancel()
	if err := s.start(ctx, id); err != nil {
		return serving, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		instance, err := s.registry.Read(ctx, id)
		if err == nil {
			if instance.Version != s.version || instance.BuildIdentity != s.build {
				return serving, errors.New("local runner started with a different version or executable")
			}
			alive, err := s.alive(instance)
			if err != nil {
				return serving, err
			}
			if !alive || instance.State == "stopped" || instance.State == "draining" {
				return serving, errors.New("local runner stopped before activation")
			}
			if instance.State == "active" {
				return true, nil
			}
		} else if !localRunnerNotFound(err) {
			return serving, err
		}
		select {
		case <-ctx.Done():
			return serving, fmt.Errorf("local runner %s did not activate: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// safety: a runner a previous controller started under another budget is replaced, so a changed
// admission.budget applies after the controller restarts.
func (s localRunnerProcessSupervisor) limitsCurrent(instance localhostwire.LocalRunnerInstance) bool {
	if s.applied == nil || s.started[instance.ID] {
		return true
	}
	applied, ok := s.applied(instance)
	return !ok || applied.Matches(s.limits)
}

func localRunnerNotFound(err error) bool {
	var failure *apierr.Error
	return errors.As(err, &failure) && failure.Code == apierr.NotFound
}

func localRunnerLimits() wingd.RunnerLimits {
	budget, err := wingd.ResolveBudget("")
	if err == nil {
		var limits wingd.RunnerLimits
		if limits, err = wingd.LocalRunnerLimits(budget.Budget); err == nil {
			return limits
		}
	}
	slog.Warn("local runner starts without the budget's OS limits", "err", err)
	return wingd.RunnerLimits{}
}
