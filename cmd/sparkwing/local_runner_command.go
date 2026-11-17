package main

import (
	"context"
	"errors"
	"fmt"
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
			_ = conn.Close()
			return true
		}, probe.C, localRunnerOrphanAfter)
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
	// controller missing this long is gone, and every retry against it would
	// keep the runner's service alive with no owner.
	localRunnerOrphanAfter = 10 * time.Minute
)

// awaitControllerGone returns an error once the controller of home has been
// unreachable on every probe for orphanAfter, or at once when home is gone.
// It returns nil when ctx ends first.
func awaitControllerGone(ctx context.Context, home string, reachable func(context.Context) bool, ticks <-chan time.Time, orphanAfter time.Duration) error {
	var since time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticks:
			if _, err := os.Stat(home); errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("local runner exits: its state home %s is gone", home)
			}
			if reachable(ctx) {
				since = time.Time{}
				continue
			}
			if since.IsZero() {
				since = now
			}
			if now.Sub(since) >= orphanAfter {
				return fmt.Errorf("local runner exits: no controller has served %s for %s", home, now.Sub(since).Round(time.Second))
			}
		}
	}
}
