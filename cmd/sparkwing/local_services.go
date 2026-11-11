package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const localAdmissionTimeout = 3 * time.Second

func resolveLocalServices(ctx context.Context) (dashboardServices, error) {
	p, err := paths.DefaultPaths()
	if err != nil {
		return dashboardServices{}, err
	}
	return resolveLocalServicesAt(ctx, p.Root)
}

func resolveLocalServicesAt(ctx context.Context, home string) (dashboardServices, error) {
	return resolveLocalServicesWith(ctx, home, openLocalController)
}

func resolveLocalServicesWith(ctx context.Context, home string, open func(context.Context, string) (wingd.LocalController, error)) (dashboardServices, error) {
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
	config.Open = open
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
	// safety: the service manager must not substitute an inherited inline key.
	environment := map[string]string{
		"HOME": userHome, "XDG_CONFIG_HOME": configHome,
		"SPARKWING_SECRETS_KEY":          "",
		"SPARKWING_SECRETS_PREVIOUS_KEY": "",
	}
	configPath, err := userconfig.Path()
	if err != nil {
		return crontimer.Host{}, err
	}
	keyPath := os.Getenv(localsecrets.KeyFileEnv)
	if keyPath == "" {
		keyPath = filepath.Join(configHome, "sparkwing", "secrets.key")
	}
	for name, value := range map[string]string{userconfig.PathEnv: configPath, localsecrets.KeyFileEnv: keyPath} {
		environment[name], err = filepath.Abs(value)
		if err != nil {
			return crontimer.Host{}, err
		}
	}
	nativeHome, err := nativeServiceHome(runtime.GOOS, userHome)
	if err != nil {
		return crontimer.Host{}, err
	}
	return crontimer.Host{
		GOOS: runtime.GOOS, Home: nativeHome, ConfigHome: configHome,
		SparkwingHome: home, Binary: binary, Version: installedVersion(),
		PathEnv: os.Getenv("PATH"), Env: environment, UID: os.Getuid(), Exec: crontimer.DefaultExec,
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
	dashboard, err := wingd.ResolveDashboardSettings()
	if err != nil {
		return wingd.LocalRuntimeConfig{}, err
	}
	return wingd.LocalRuntimeConfig{Admission: wingd.Config{
		Home: home, Version: installedVersion(), Budget: budget.Budget,
		BudgetSource: budget.Source, BudgetOrigin: budget.Origin, AdmissionPolicy: &policy,
	}, Dashboard: dashboard, Open: openLocalController}, nil
}

func openLocalController(ctx context.Context, home string) (wingd.LocalController, error) {
	return (localcontroller.Config{Bind: bindLocalController}).Open(ctx, home)
}

func bindLocalController(ctx context.Context, b localcontroller.Binding) (localcontroller.Services, error) {
	return bindLocalControllerWithClock(ctx, b, nil)
}

func bindLocalControllerWithClock(ctx context.Context, b localcontroller.Binding, now func() time.Time) (_ localcontroller.Services, err error) {
	keyring, err := localsecrets.LoadKeyring(localsecrets.KeyringOptions{Create: true})
	if err != nil {
		return localcontroller.Services{}, err
	}
	cipher := keyring.For(b.Store.AsOperator())
	state, err := localcontroller.NewState(b)
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
	key, err := b.Store.AsOperator().LocalObjectSigningKey(ctx, cipher)
	if err != nil {
		return localcontroller.Services{}, err
	}
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: filepath.Join(b.Home, "data"), Endpoint: wingdclient.APIBaseURL, SigningKey: key, LogNow: now})
	if err != nil {
		return localcontroller.Services{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, objects.Close())
		}
	}()
	var machineRunner userconfig.RunnerConfig
	configPath, _, err := userconfig.ReadDefault(userconfig.Runner, &machineRunner)
	if err != nil {
		return localcontroller.Services{}, err
	}
	warnIgnoredRunnerLabels(configPath)
	profile, err := localcontroller.NewClaimProfile(machineRunner.Labels)
	if err != nil {
		return localcontroller.Services{}, err
	}

	server := controller.New(b.Store, nil).WithEngineController(b.Controller).WithSecretsCipher(cipher).WithDataObjects(objects).
		WithDashboard(controller.Dashboard{Local: true, Version: installedVersion(), Paths: paths.PathsAt(b.Home), Bundle: web.VerifiedBundle()})
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(b.Home, "environments"))
	if err != nil {
		return localcontroller.Services{}, err
	}
	schedules, err := localcontroller.NewScheduleService(b, installedVersion(), server.ScheduleLauncher())
	if err != nil {
		return localcontroller.Services{}, err
	}
	timerHost, err := localServiceHost(b.Home)
	if err != nil {
		return localcontroller.Services{}, err
	}
	schedules.NativeTimer = &timerHost
	server.WithLocalSessions(paths.PathsAt(b.Home).SessionLedgerDir())
	server.WithLocalRunner(controller.LocalRunner{Name: localRunnerName(), Labels: match.SelfAsserted(machineRunner.Labels)})
	server.WithLocalAdmission(func(ctx context.Context) (wingwire.QueueState, error) {
		// safety: a daemon can hold its socket and stop answering HTTP; the bound answers the
		// console's poll with the public refusal well before the browser abandons the request at ten seconds.
		ctx, cancel := context.WithTimeout(ctx, localAdmissionTimeout)
		defer cancel()
		state, err := wingdclient.Query(ctx, wingdclient.Options{Home: b.Home, Version: installedVersion()})
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			return wingwire.QueueState{}, nil
		}
		return state, err
	})
	server.WithLocalSchedules(schedules, func(ctx context.Context, root, pipeline string, environment []string) (crons.Proof, error) {
		return proveLocalSchedule(ctx, b.Home, root, pipeline, environment)
	}, snapshots)
	policies := server.StorePolicies()
	policies.ClaimProfiles = profile
	policies.LocalSecrets = cipher
	return localcontroller.Services{
		Policies: policies,
		State:    state, HTTP: server.HTTP(), Close: objects.Close,
		Maintain: server.MaintainStorage,
		Complete: server.ReconcileRunEffects,
		Runner: func(ctx context.Context) error {
			return superviseLocalRunnerProcess(ctx, timerHost)
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
	result.mintLogin = func(ctx context.Context, host string) (wingd.DashboardLogin, error) {
		return wingdclient.MintDashboardLogin(ctx, home, host)
	}
	result.retireRunner = func(ctx context.Context) error {
		deadline := time.Now().Add(wingd.LocalRunnerRetireWait)
		err := wingdclient.Stop(ctx, wingdclient.Options{Home: home, RetireRunner: true})
		if err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
			return err
		}
		// safety: the joined controller cannot launch another runner after this inventory.
		wait, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		return wingd.WaitLocalRunnerShutdown(wait, host)
	}
	return result, nil
}

func warnIgnoredRunnerLabels(path string) {
	ignored, err := userconfig.IgnoredRunnerLabels(path)
	if err != nil {
		slog.Warn("could not inspect runner.labels", "err", err)
	}
	for _, label := range ignored {
		slog.Warn(label.String())
	}
}

func localRunnerName() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "local"
}

func nativeServiceHome(goos, callerHome string) (string, error) {
	if goos != "darwin" {
		return callerHome, nil
	}
	// safety: launchd discovers login agents under the account home, regardless of HOME.
	account, err := user.Current()
	if err != nil {
		return "", err
	}
	return account.HomeDir, nil
}
