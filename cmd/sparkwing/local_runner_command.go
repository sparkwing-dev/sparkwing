package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

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
	// safety: a service stop drains claims without cancelling held jobs or terminal uploads.
	return runLocalQueue(context.Background(), id, options, loop, lifecycle)
}
