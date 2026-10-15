package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
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
	userHome, err := os.UserHomeDir()
	if err != nil {
		return dashboardServices{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return dashboardServices{}, err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(userHome, ".config")
	}
	configHome, err = filepath.Abs(configHome)
	if err != nil {
		return dashboardServices{}, err
	}
	host := crontimer.Host{
		GOOS: runtime.GOOS, Home: userHome, ConfigHome: configHome,
		SparkwingHome: home, Binary: binary, Version: installedVersion(),
		PathEnv: os.Getenv("PATH"), UID: os.Getuid(), Exec: crontimer.DefaultExec,
	}
	config, err := resolveLocalRuntimeConfig(home)
	if err != nil {
		return dashboardServices{}, err
	}
	return composeLocalServices(ctx, config, host)
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

func openLocalController(context.Context, string) (wingd.LocalController, error) {
	return nil, &apierr.Error{Code: apierr.Unavailable, Message: "elected local controller factory is unavailable in this build", Hint: "bind the shared controller, store, queue and maintenance through wingd.LocalController"}
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
