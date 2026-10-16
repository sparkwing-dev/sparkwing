package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type localScheduleRegistration = controllerwire.LocalScheduleRegistration

type localScheduleServices struct {
	lifecycle      wingd.LocalLifecycle
	units          wingd.ManagedUnitManager
	options        wingd.LocalStartOptions
	register       func(context.Context, localScheduleRegistration) (controllerwire.ScheduleRepoWritten, error)
	pin            func(context.Context, string, bool) (controllerwire.ScheduleView, error)
	tick           func(context.Context, bool) (controllerwire.TickView, error)
	installTimer   func(context.Context, wingd.LocalStartOptions) error
	uninstallTimer func(context.Context) error
}

func resolveLocalScheduleServices(ctx context.Context, home string) (localScheduleServices, error) {
	if err := context.Cause(ctx); err != nil {
		return localScheduleServices{}, err
	}
	home, err := wingd.HomeDir(home)
	if err != nil {
		return localScheduleServices{}, err
	}
	host, err := localServiceHost(home)
	if err != nil {
		return localScheduleServices{}, err
	}
	config, err := resolveLocalRuntimeConfig(home)
	if err != nil {
		return localScheduleServices{}, err
	}
	services, err := composeLocalServices(ctx, config, host)
	if err != nil {
		return localScheduleServices{}, err
	}
	return localScheduleServices{
		lifecycle: services.lifecycle, units: services.units, options: services.options,
		register: func(ctx context.Context, input localScheduleRegistration) (controllerwire.ScheduleRepoWritten, error) {
			manager, closeTransport, err := localScheduleTransport(ctx, home)
			if err != nil {
				return controllerwire.ScheduleRepoWritten{}, err
			}
			defer closeTransport()
			return manager.RegisterLocalSchedules(ctx, input)
		},
		pin: func(ctx context.Context, id string, pin bool) (controllerwire.ScheduleView, error) {
			manager, closeTransport, err := localScheduleTransport(ctx, home)
			if err != nil {
				return controllerwire.ScheduleView{}, err
			}
			defer closeTransport()
			return manager.SetLocalSchedulePinned(ctx, id, pin)
		},
		tick: func(ctx context.Context, dryRun bool) (controllerwire.TickView, error) {
			manager, closeTransport, err := localScheduleTransport(ctx, home)
			if err != nil {
				return controllerwire.TickView{}, err
			}
			defer closeTransport()
			return manager.TickLocalSchedules(ctx, dryRun)
		},
		installTimer: func(ctx context.Context, options wingd.LocalStartOptions) error {
			if options.Home != host.SparkwingHome || options.Executable != host.Binary || options.Version != host.Version {
				return fmt.Errorf("schedule timer differs from the captured local controller")
			}
			_, err := crontimer.Install(ctx, host)
			return err
		},
		uninstallTimer: func(ctx context.Context) error {
			_, err := crontimer.Uninstall(ctx, host)
			return err
		},
	}, nil
}

func validateLocalSchedules(services localScheduleServices) error {
	if services.lifecycle == nil {
		return scheduleUnavailable("the local controller lifecycle")
	}
	if !filepath.IsAbs(services.options.Home) {
		return scheduleUnavailable("an absolute captured state home")
	}
	return nil
}

func prepareScheduleTimer(ctx context.Context, services localScheduleServices, goos string, install bool) (func(context.Context) error, error) {
	if goos == "windows" {
		return func(context.Context) error { return nil }, nil
	}
	if services.units == nil {
		return nil, scheduleUnavailable("the managed-unit manager")
	}
	units, err := services.units.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	installed := false
	for _, unit := range units {
		if unit.Kind == wingd.ManagedSchedule {
			if unit.Home != services.options.Home {
				return nil, fmt.Errorf("schedule timer belongs to another state home")
			}
			installed = true
		}
	}
	if install && installed {
		return func(ctx context.Context) error {
			return services.units.Refresh(ctx, services.options.Executable, services.options.Version)
		}, nil
	}
	if install {
		if services.installTimer == nil {
			return nil, scheduleUnavailable("native schedule timer installation")
		}
		return func(ctx context.Context) error { return services.installTimer(ctx, services.options) }, nil
	}
	if installed {
		if services.uninstallTimer == nil {
			return nil, scheduleUnavailable("native schedule timer removal")
		}
		return services.uninstallTimer, nil
	}
	return func(context.Context) error { return nil }, nil
}

func installLocalSchedules(ctx context.Context, inv clisurface.Invocation, opts scheduleOptions, out io.Writer, command scheduleCommand) error {
	services, err := command.local(ctx, "")
	if err != nil {
		return err
	}
	if err := validateLocalSchedules(services); err != nil {
		return err
	}
	if services.register == nil {
		return scheduleUnavailable("checkout, environment and pin registration")
	}
	roots, err := command.roots(inv.Bool("all-repos"), false)
	if err != nil {
		return err
	}
	var requests []localScheduleRegistration
	var skipped [][]string
	count := 0
	environment := slices.Clone(command.environment())
	for _, root := range roots {
		cfg, err := command.load(root)
		if err != nil {
			return err
		}
		declarations, other, err := selectScheduleDeclarations(cfg, inv.Flags["schedule"], "local")
		if err != nil {
			return err
		}
		requests = append(requests, localScheduleRegistration{
			Mode: scheduleWriteMode(inv), RepoPath: root, Project: cfg, Schedules: declarations,
			Environment: slices.Clone(environment), Follow: inv.Bool("unpinned") || inv.Bool("no-prove"), Prove: !inv.Bool("no-prove"),
		})
		skipped = append(skipped, other)
		count += len(declarations)
	}
	var timer func(context.Context) error
	if count > 0 && !inv.Bool("no-timer") {
		timer, err = prepareScheduleTimer(ctx, services, command.goos, true)
		if err != nil {
			return err
		}
	}
	if _, err := ensureLocalController(ctx, services.options.Home, func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: services.lifecycle, options: services.options}, nil
	}); err != nil {
		return err
	}
	for i, request := range requests {
		var result controllerwire.ScheduleRepoWritten
		if request.Mode != controllerwire.ScheduleUpsert || len(request.Schedules) > 0 {
			result, err = services.register(ctx, request)
			if err != nil {
				return err
			}
		}
		if err := renderScheduleInstallation(out, scheduleInstallation{Repo: request.RepoPath, ScheduleRepoWritten: result, Skipped: skipped[i]}, opts.format); err != nil {
			return err
		}
	}
	if timer != nil {
		return timer(ctx)
	}
	return nil
}

func changeLocalSchedules(ctx context.Context, inv clisurface.Invocation, opts scheduleOptions, out io.Writer, command scheduleCommand) error {
	services, err := command.local(ctx, "")
	if err != nil {
		return err
	}
	if err := validateLocalSchedules(services); err != nil {
		return err
	}
	uninstall := strings.HasSuffix(inv.Command.Path, " uninstall")
	if !uninstall && services.pin == nil {
		return scheduleUnavailable("local pin proofs and pin transactions")
	}
	var timer func(context.Context) error
	var root string
	if uninstall {
		timer, err = prepareScheduleTimer(ctx, services, command.goos, false)
		if err != nil {
			return err
		}
		if !inv.Bool("all-repos") && len(inv.Flags["schedule"]) == 0 {
			roots, err := command.roots(false, true)
			if err != nil {
				return err
			}
			root = roots[0]
		}
	}
	if _, err := ensureLocalController(ctx, services.options.Home, func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: services.lifecycle, options: services.options}, nil
	}); err != nil {
		return err
	}
	connection, err := command.connect(ctx, "")
	if err != nil {
		return err
	}
	defer connection.close()
	rows, err := selectSchedules(ctx, inv, opts, connection.manager)
	if err != nil {
		return err
	}
	if uninstall {
		all, err := connection.manager.ListSchedules(ctx, controllerwire.ScheduleQuery{All: true})
		if err != nil {
			return err
		}
		if len(inv.Flags["schedule"]) == 0 {
			for _, row := range all.Schedules {
				if row.Where == "local" && (inv.Bool("all-repos") || filepath.Clean(row.RepoPath) == root) {
					rows = append(rows, controllerwire.DetailView{Schedule: row})
				}
			}
		}
	}
	for _, row := range rows {
		var result any
		if uninstall {
			result, err = connection.manager.DeleteSchedule(ctx, row.Schedule.ID)
		} else {
			result, err = services.pin(ctx, row.Schedule.ID, opts.action == "pin")
		}
		if err != nil {
			return err
		}
		if err := renderScheduleRecord(out, result, row.Schedule.ID, opts.format); err != nil {
			return err
		}
	}
	if uninstall {
		remaining, err := connection.manager.ListSchedules(ctx, controllerwire.ScheduleQuery{All: true})
		if err != nil {
			return err
		}
		if len(remaining.Schedules) == 0 {
			return timer(ctx)
		}
	}
	return nil
}

func runScheduleTick(args []string) error {
	return runScheduleTickCommand(context.Background(), args, os.Stdout, resolveLocalScheduleServices)
}

func runScheduleTickCommand(ctx context.Context, args []string, out io.Writer, resolve func(context.Context, string) (localScheduleServices, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inv, err := clisurface.Parse("sparkwing", append([]string{"__tick"}, args...))
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	if inv.Has("home") && !filepath.IsAbs(inv.Value("home")) {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--home requires an absolute state home"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	services, err := resolve(ctx, inv.Value("home"))
	if err != nil {
		return err
	}
	if err := validateLocalSchedules(services); err != nil {
		return err
	}
	if inv.Has("home") && filepath.Clean(inv.Value("home")) != filepath.Clean(services.options.Home) {
		return scheduleUnavailable("the state home captured by the timer")
	}
	if services.tick == nil {
		return scheduleUnavailable("the controller-owned tick operation")
	}
	if _, err := ensureLocalController(ctx, services.options.Home, func(context.Context) (dashboardServices, error) {
		return dashboardServices{lifecycle: services.lifecycle, options: services.options}, nil
	}); err != nil {
		return err
	}
	result, err := services.tick(ctx, inv.Bool("dry-run"))
	if err != nil {
		return err
	}
	if format == "json" {
		err = json.NewEncoder(out).Encode(result)
	} else {
		_, err = fmt.Fprintln(out, result.At)
	}
	if err == nil && result.Error != "" {
		err = fmt.Errorf("schedule tick: %s", result.Error)
	}
	return err
}

func localScheduleTransport(ctx context.Context, home string) (controllerwire.LocalScheduleManager, func(), error) {
	options, closeTransport, err := localControllerOptions(ctx, home, wingdclient.QueryLocalController, func(ctx context.Context) (dashboardServices, error) { return resolveLocalServicesAt(ctx, home) })
	if err != nil {
		return nil, nil, err
	}
	manager, err := client.NewLocalScheduleManager(options)
	if err != nil {
		closeTransport()
		return nil, nil, err
	}
	return manager, closeTransport, nil
}

func proveLocalSchedule(ctx context.Context, home, root, pipeline string, environment []string) (crons.Proof, error) {
	options, err := localSourceOptions(home, environment, secrets.NewMasker())
	if err != nil {
		return crons.Proof{}, err
	}
	directory := filepath.Join(root, ".sparkwing")
	if typescript, err := pipelineentry.IsTypeScript(directory); err != nil {
		return crons.Proof{}, err
	} else if typescript {
		options.SDK = engine.TypeScriptSDK
	}
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: root})
	if err != nil {
		return crons.Proof{}, err
	}
	entry, err := source.Acquire(ctx, source.Spec{RepoKey: string(repository.Key), LocalCheckout: root, PipelineDir: ".sparkwing"}, options)
	if err != nil {
		if entry != nil {
			err = errors.Join(err, entry.Close())
		}
		return crons.Proof{}, err
	}
	if entry == nil {
		return crons.Proof{}, errors.New("schedule source acquisition returned no entry")
	}
	if len(entry.Command) != 1 {
		return crons.Proof{}, errors.Join(fmt.Errorf("%s: schedule pinning requires a standalone pipeline executable", pipeline), entry.Close())
	}
	if err := writeEngineNotices(os.Stderr, entry.Engine.Notices); err != nil {
		return crons.Proof{}, errors.Join(err, entry.Close())
	}
	return crons.Proof{Binary: entry.Command[0], Release: entry.Close}, nil
}
