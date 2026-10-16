package main

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func resolveLocalServices(ctx context.Context) (dashboardServices, error) {
	p, err := paths.DefaultPaths()
	if err != nil {
		return dashboardServices{}, err
	}
	return resolveLocalServicesAt(ctx, p.Root)
}

func resolveLocalServicesAt(ctx context.Context, home string) (dashboardServices, error) {
	if err := ctx.Err(); err != nil {
		return dashboardServices{}, err
	}
	home, err := wingd.HomeDir(home)
	if err != nil {
		return dashboardServices{}, err
	}
	host, err := localServiceHost(home)
	if err != nil {
		return dashboardServices{}, err
	}
	config, err := resolveLocalRuntimeConfig(home)
	if err != nil {
		return dashboardServices{}, err
	}
	return composeLocalServices(ctx, config, host)
}

func localServiceHost(home string) (crontimer.Host, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return crontimer.Host{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return crontimer.Host{}, err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(userHome, ".config")
	}
	configHome, err = filepath.Abs(configHome)
	if err != nil {
		return crontimer.Host{}, err
	}
	return crontimer.Host{
		GOOS: runtime.GOOS, Home: userHome, ConfigHome: configHome,
		SparkwingHome: home, Binary: binary, Version: installedVersion(),
		PathEnv: os.Getenv("PATH"), UID: os.Getuid(), Exec: crontimer.DefaultExec,
	}, nil
}

func resolveLocalRuntimeConfig(home string) (wingd.LocalRuntimeConfig, error) {
	budget, err := wingd.ResolveBudget("")
	if err != nil {
		return wingd.LocalRuntimeConfig{}, err
	}
	policy, _, err := wingd.ResolveAdmissionPolicy()
	if err != nil {
		return wingd.LocalRuntimeConfig{}, err
	}
	return wingd.LocalRuntimeConfig{Admission: wingd.Config{
		Home: home, Version: installedVersion(), Budget: budget.Budget,
		BudgetSource: budget.Source, BudgetOrigin: budget.Origin, AdmissionPolicy: &policy,
	}, Open: openLocalController}, nil
}

func openLocalController(ctx context.Context, home string) (wingd.LocalController, error) {
	return (localcontroller.Config{Bind: bindLocalController}).Open(ctx, home)
}

func bindLocalController(ctx context.Context, b localcontroller.Binding) (_ localcontroller.Services, err error) {
	keyring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{Create: true})
	if err != nil {
		return localcontroller.Services{}, err
	}
	cipher := keyring.For(b.Store)
	state, err := localcontroller.NewState(b, cipher)
	if err != nil {
		return localcontroller.Services{}, err
	}
	engines, err := engine.NewResolver(engine.Config{BuildVersion: installedVersion(), CacheDirectory: filepath.Join(b.Home, "engines")})
	if err != nil {
		return localcontroller.Services{}, err
	}
	b.Controller.RunnerPolicy = &controller.SignedRunnerAdmission{Resolver: engines}
	engines, err = engine.NewResolver(engine.Config{BuildVersion: installedVersion(), CacheDirectory: filepath.Join(b.Home, "engines"), LocalOnly: true})
	if err != nil {
		return localcontroller.Services{}, err
	}
	b.Controller.VersionPolicy = &controller.SignedEngineVersionPolicy{Resolver: engines}
	b.Controller.LocalRunnerPolicy = b.Controller.VersionPolicy
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return localcontroller.Services{}, err
	}
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: filepath.Join(b.Home, "data"), Endpoint: wingdclient.APIBaseURL, SigningKey: key})
	if err != nil {
		return localcontroller.Services{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, objects.Close())
		}
	}()
	claimant, err := localcontroller.NewClaimant(b.Store)
	if err != nil {
		return localcontroller.Services{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, claimant.Close())
		}
	}()
	b.Store.SetClaimProfileResolver(claimant)
	server := controller.New(b.Store, nil).WithEngineController(b.Controller).WithSecretsCipher(cipher).WithDataObjects(objects).
		WithDashboard(controller.Dashboard{Local: true, Version: installedVersion(), Paths: paths.PathsAt(b.Home), Bundle: web.BundleFS()})
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(b.Home, "environments"))
	if err != nil {
		return localcontroller.Services{}, err
	}
	schedules, err := localcontroller.NewScheduleService(b, installedVersion(), server.ScheduleLauncher())
	if err != nil {
		return localcontroller.Services{}, err
	}
	server.WithLocalSessions(paths.PathsAt(b.Home).SessionLedgerDir())
	server.WithLocalSchedules(schedules, func(ctx context.Context, root, pipeline string, environment []string) (crons.Proof, error) {
		return proveLocalSchedule(ctx, b.Home, root, pipeline, environment)
	}, snapshots)
	socket, err := wingd.APISocketPath(b.Home)
	if err != nil {
		return localcontroller.Services{}, err
	}
	transport := wingdclient.NewAPIClient(socket)
	return localcontroller.Services{
		State: state, HTTP: server.HTTP(), Close: func() error {
			transport.CloseIdleConnections()
			return errors.Join(claimant.Close(), objects.Close())
		},
		Maintain: server.MaintainStorage,
		Runner: runners.StoreRunnerConfig{
			Bearer:          claimant.Bearer,
			Loop:            runners.LoopConfig{Version: installedVersion(), MaxConcurrent: runtime.NumCPU()},
			RequestCapacity: func() (float64, int64) { return float64(runtime.NumCPU()), 0 },
			Bind: func(ctx context.Context, binding runners.StoreBinding) (runners.NativeServices, error) {
				return newLocalNativeServices(ctx, b.Home, b.Store, binding, client.EngineOptions{URL: wingdclient.APIBaseURL, HTTP: transport})
			},
		},
	}, nil
}

func composeLocalServices(ctx context.Context, config wingd.LocalRuntimeConfig, host crontimer.Host) (dashboardServices, error) {
	var result dashboardServices
	if err := ctx.Err(); err != nil {
		return result, err
	}
	home, err := wingd.HomeDir(config.Admission.Home)
	if err != nil {
		return result, err
	}
	if filepath.Clean(host.SparkwingHome) != home {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "local runtime and managed units must use the same home"}
	}
	config.Admission.Home = home
	result.runtime, err = wingd.NewLocalRuntime(config)
	if err != nil {
		return dashboardServices{}, err
	}
	result.units, err = wingd.NewManagedUnitManager(host)
	if err != nil {
		return dashboardServices{}, err
	}
	result.lifecycle, err = wingdclient.NewLocalLifecycle(home, result.runtime, result.units)
	if err != nil {
		return dashboardServices{}, err
	}
	log, err := wingd.LogPath(home)
	if err != nil {
		return dashboardServices{}, err
	}
	result.openLog = func(ctx context.Context) (*os.File, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return fssecure.OpenPrivateConfig(log)
	}
	result.options = wingd.LocalStartOptions{Home: home, Executable: host.Binary, Version: host.Version}
	return result, nil
}
