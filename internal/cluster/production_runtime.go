package cluster

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// ProductionRuntime composes installed commands with authenticated controller and native host services.
func ProductionRuntime(version string) Runtime {
	return Runtime{
		Launcher: productionLauncher(version),
		Runner: RunnerCLI{Factory: func(ctx context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
			home, err := wingd.HomeDir("")
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			engines, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: filepath.Join(home, "engines")})
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			capacity, err := runnerCapacity(home, version, options.Config, engines)
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			hosting := &productionHost{UnitHost: localcontroller.RunnerHosting(home, version, client.EngineOptions{URL: *options.Config.Controller}, "", "", options.Config.LocalAdmission != nil && *options.Config.LocalAdmission)}
			installation, err := productionInstallation(options.ConfigPath, hosting)
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			return runners.RuntimeConfig{
				Installation: installation, Versions: engines, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
				Hosting: hosting, RequestCapacity: capacity,
			}, nil
		}},
		Bootstrap: BootstrapRuntime{
			Source: func(_ context.Context, options BootstrapOptions) (launcher.SourceInit, error) {
				return launcher.SourceInit{Bind: func(_ context.Context, award launcher.CloudJobAward) (source.Options, func() error, error) {
					access, err := client.NewSourceAccess(client.EngineOptions{URL: options.ControllerURL, Bearer: fixedBearer(string(award.Bootstrap))}, award.Binding.RunID, award.Source)
					return source.Options{Access: access, Base: procenv.Base{Ambient: os.Environ(), Masker: secrets.NewMasker()}}, nil, err
				}}, nil
			},
			Host: func(_ context.Context, options BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
				home, err := wingd.HomeDir("")
				if err != nil {
					return launcher.BootstrapHost{}, nil, err
				}
				return launcher.BootstrapHost{Host: localcontroller.RunnerHosting(home, version, client.EngineOptions{URL: options.ControllerURL}, options.SourceDir, options.ModuleCache, false)}, nil, nil
			},
		},
	}
}

func fixedBearer(raw string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return raw, context.Cause(ctx) }
}

type productionHost struct {
	runners.UnitHost
	active atomic.Int64
}

func (h *productionHost) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	h.active.Add(1)
	defer h.active.Add(-1)
	return h.UnitHost.Host(ctx, unit)
}
