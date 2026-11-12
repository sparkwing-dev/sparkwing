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
}

func superviseLocalRunnerProcess(ctx context.Context, host crontimer.Host) error {
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
	supervisor := localRunnerProcessSupervisor{
		registry: registry, version: host.Version, build: build, alive: wingd.LocalRunnerProcessAlive,
		start: func(ctx context.Context, id string) error { return startLocalRunnerProcess(ctx, host, id) },
	}
	return supervisor.run(ctx)
}

func (s localRunnerProcessSupervisor) run(ctx context.Context) error {
	delay := localRunnerCheckInterval
	for {
		// safety: runs hosted by the CLI need no runner, so a runner that cannot start must not stop
		// the controller; queued jobs wait and the controller log names the cause.
		if err := s.ensure(ctx); err != nil && ctx.Err() == nil {
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

func (s localRunnerProcessSupervisor) ensure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	instance, err := s.registry.Active(ctx)
	if err == nil {
		alive, err := s.alive(instance)
		if err != nil {
			return fmt.Errorf("inspect local runner %s: %w", instance.ID, err)
		}
		if alive && instance.Version == s.version && instance.BuildIdentity == s.build {
			return nil
		}
	} else if !localRunnerNotFound(err) {
		return err
	}
	id := rand.Text()
	ctx, cancel := context.WithTimeout(ctx, wingdclient.RequestTimeout)
	defer cancel()
	if err := s.start(ctx, id); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		instance, err := s.registry.Read(ctx, id)
		if err == nil {
			if instance.Version != s.version || instance.BuildIdentity != s.build {
				return errors.New("local runner started with a different version or executable")
			}
			alive, err := s.alive(instance)
			if err != nil {
				return err
			}
			if !alive || instance.State == "stopped" || instance.State == "draining" {
				return errors.New("local runner stopped before activation")
			}
			if instance.State == "active" {
				return nil
			}
		} else if !localRunnerNotFound(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("local runner %s did not activate: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

func localRunnerNotFound(err error) bool {
	var failure *apierr.Error
	return errors.As(err, &failure) && failure.Code == apierr.NotFound
}
