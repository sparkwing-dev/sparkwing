package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func runLocalRunnerCommand(inv clisurface.Invocation) error {
	home, id := inv.Value("home"), inv.Value("instance")
	if !filepath.IsAbs(home) || strings.TrimSpace(id) == "" {
		return errors.New("local runner requires an absolute home and instance identity")
	}
	home, err := wingd.HomeDir(home)
	if err != nil {
		return err
	}
	if err := wingd.RemoveLocalRunnerStartupEnvironment(home, id); err != nil {
		return err
	}
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		return err
	}
	transport := wingdclient.NewAPIClient(socket)
	defer transport.CloseIdleConnections()
	options := client.EngineOptions{URL: wingdclient.APIBaseURL, HTTP: transport}
	resolver, err := engine.NewResolver(engine.Config{BuildVersion: installedVersion(), CacheDirectory: filepath.Join(home, "engines"), LocalOnly: true})
	if err != nil {
		return err
	}
	policy := controller.SignedEngineVersionPolicy{Resolver: resolver}
	lifecycle, err := wingdclient.NewLocalRunnerClient(home, installedVersion())
	if err != nil {
		return err
	}
	loop := &runners.Loop{
		Config:       runners.LoopConfig{Version: installedVersion(), MaxConcurrent: runtime.NumCPU()},
		CheckVersion: policy.CheckRunner, Host: localRunnerHosting(home, options),
	}
	stopping, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-stopping.Done():
			loop.Drain()
		case <-done:
		}
	}()
	defer func() { close(done); <-joined }()
	queue := make(chan error, 1)
	go func() {
		// safety: a service stop drains claims without cancelling held jobs or terminal uploads.
		queue <- runLocalQueue(context.Background(), id, options, loop, lifecycle)
	}()
	probing, stopProbing := context.WithCancel(context.Background())
	defer stopProbing()
	probe := time.NewTicker(localRunnerControllerProbe)
	defer probe.Stop()
	orphaned := make(chan error, 1)
	go func() {
		orphaned <- awaitControllerGone(probing, home, func(ctx context.Context) bool {
			conn, err := wingd.DialSocket(ctx, socket, localRunnerControllerProbe)
			if err != nil {
				return false
			}
			if err := conn.Close(); err != nil {
				slog.Warn("close the controller probe", "err", err)
			}
			return true
		}, probe.C, localRunnerOrphanAfter, localRunnerHomeGoneAfter)
	}()
	select {
	case err := <-queue:
		return err
	case err := <-orphaned:
		return err
	}
}

const (
	localRunnerControllerProbe = 30 * time.Second
	// safety: a replacement controller binds the socket within seconds, so a
	// socket refusing connections this long has no controller behind it, and
	// every retry against it would keep the runner's service alive with no owner.
	localRunnerOrphanAfter = 10 * time.Minute
	// safety: a home missing on every probe while the socket also refuses is
	// deletion rather than a remount, so it needs a shorter wait, never none.
	localRunnerHomeGoneAfter = time.Minute
)

// safety: reachable reports only that the socket accepts a connection, so a
// controller that answers keeps the runner alive whatever its home looks like.
func awaitControllerGone(ctx context.Context, home string, reachable func(context.Context) bool, ticks <-chan time.Time, orphanAfter, homeGoneAfter time.Duration) error {
	var unreachableSince, homeGoneSince time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticks:
			if reachable(ctx) {
				unreachableSince, homeGoneSince = time.Time{}, time.Time{}
				continue
			}
			if unreachableSince.IsZero() {
				unreachableSince = now
			}
			if _, err := os.Stat(home); errors.Is(err, os.ErrNotExist) {
				if homeGoneSince.IsZero() {
					homeGoneSince = now
				}
				if now.Sub(homeGoneSince) >= homeGoneAfter {
					return fmt.Errorf("local runner exits: its state home %s has been gone and its controller socket closed for %s", home, now.Sub(homeGoneSince).Round(time.Second))
				}
			} else {
				homeGoneSince = time.Time{}
			}
			if now.Sub(unreachableSince) >= orphanAfter {
				return fmt.Errorf("local runner exits: no controller has accepted a connection on the socket of %s for %s", home, now.Sub(unreachableSince).Round(time.Second))
			}
		}
	}
}
