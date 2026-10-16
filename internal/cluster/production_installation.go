package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/service"
)

func productionInstallation(config string, hosting *productionHost) (runners.RunnerInstallation, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	return runners.NewNativeInstallation(runners.NativeInstallationConfig{
		Executable: executable,
		Drain: func(ctx context.Context) error {
			if err := context.Cause(ctx); err != nil {
				return err
			}
			if hosting.active.Load() != 0 {
				return errors.New("runner replacement requires joined claim hosts")
			}
			return nil
		},
		RefreshManagedUnits: func(ctx context.Context, _ string) error {
			if config == "" {
				return context.Cause(ctx)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			configHome, err := os.UserConfigDir()
			if err != nil {
				return err
			}
			return service.RefreshRegistration(ctx, service.Host{GOOS: runtime.GOOS, Home: home, ConfigHome: configHome, Binary: executable, ConfigPath: config, UID: os.Getuid(), Exec: service.DefaultExec})
		},
		Restart: func(ctx context.Context) error {
			if err := context.Cause(ctx); err != nil {
				return err
			}
			return restartRunner(executable, os.Args[1:], os.Environ())
		},
	})
}
