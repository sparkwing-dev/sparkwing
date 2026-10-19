// Package controllerhost runs a self-hosted controller's own claimant, so a
// controller with no registered runners still hosts run-anywhere work.
package controllerhost

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Config selects whether and where a controller hosts units itself.
type Config struct {
	// Cloud marks a deployment whose launcher hosts unselected runs; it composes no host claimant.
	Cloud bool
	// Home holds the host's engine cache and workspaces.
	Home    string
	Version string
}

// Serve runs srv at addr and, on a self-hosted deployment, the host claimant beside it.
func Serve(ctx context.Context, srv *controller.Server, addr string, cfg Config) error {
	host, err := New(ctx, srv, cfg)
	if err != nil {
		return err
	}
	if host == nil {
		return controller.ServeWith(ctx, srv, addr)
	}
	ctx, cancel := context.WithCancel(ctx)
	hosted := make(chan error, 1)
	go func() { hosted <- host.Run(ctx) }()
	err = controller.ServeWith(ctx, srv, addr)
	cancel()
	return errors.Join(err, <-hosted, host.Close(context.WithoutCancel(ctx)))
}

// Host claims plan, evaluation and job units with the controller's own claim root.
type Host struct {
	engine *controller.EngineController
	srv    *controller.Server
	cfg    Config
	bearer string
	prefix string
}

// New mints the host claim root of a self-hosted controller. A Cloud deployment gets nil.
func New(ctx context.Context, srv *controller.Server, cfg Config) (*Host, error) {
	if cfg.Cloud {
		return nil, nil
	}
	engine := srv.EngineController()
	if engine == nil || engine.Store == nil || cfg.Home == "" || cfg.Version == "" {
		return nil, errors.New("controller host requires the server's engine, a home and a version")
	}
	issued, err := engine.Store.IssueHostClaimant(ctx)
	if err != nil {
		return nil, err
	}
	return &Host{engine: engine, srv: srv, cfg: cfg, bearer: issued.Raw, prefix: issued.Capability.Prefix}, nil
}

// Run serves the API to award hosts on a private loopback listener and claims until ctx ends.
func (h *Host) Run(ctx context.Context) (err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: h.srv.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		closed := server.Shutdown(shutdown)
		if serveErr := <-served; !errors.Is(serveErr, http.ErrServerClosed) {
			closed = errors.Join(closed, serveErr)
		}
		err = errors.Join(err, closed)
	}()
	hosting := localcontroller.RunnerHosting(h.cfg.Home, h.cfg.Version, client.EngineOptions{URL: "http://" + listener.Addr().String()}, "", "", false)
	kinds := []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode}
	for {
		loop, err := runners.NewStoreRunner(h.engine, runners.StoreRunnerConfig{
			Bearer:  func(ctx context.Context) (string, error) { return h.bearer, context.Cause(ctx) },
			Loop:    runners.LoopConfig{Version: h.cfg.Version, MaxConcurrent: runtime.NumCPU(), Kinds: kinds},
			Host:    hosting,
			OnError: func(err error) { slog.Error("controller host unit failed", "err", err) },
			RequestCapacity: func(context.Context) (match.Resources, match.Resources, error) {
				resources := match.Resources{Cores: float64(runtime.NumCPU())}
				return resources, resources, nil
			},
		})
		if err != nil {
			return err
		}
		err = loop.Run(ctx)
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("controller host stopped claiming; restarting", "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
}

// Close revokes the host claim root after Run has returned.
func (h *Host) Close(ctx context.Context) error {
	return h.engine.Store.RevokeHostClaimant(ctx, h.prefix)
}
