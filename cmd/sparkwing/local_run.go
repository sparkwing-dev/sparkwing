package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func resolveLocalRunRuntime(ctx context.Context, options localRunOptions) (localRunRuntime, error) {
	if err := context.Cause(ctx); err != nil {
		return localRunRuntime{}, err
	}
	if options.stateDB != "" || options.standaloneReason != "" || options.environmentFile != "" {
		return localRunRuntime{}, &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "local run private host options require an elected standalone runtime"}
	}
	directory, err := findSparkwingDir()
	if err != nil {
		return localRunRuntime{}, err
	}
	home, err := wingd.HomeDir("")
	if err != nil {
		return localRunRuntime{}, err
	}
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		return localRunRuntime{}, err
	}
	captured := os.Environ()
	socket, err := wingd.APISocketPath(home)
	if err != nil {
		return localRunRuntime{}, err
	}
	transport, cleanup, err := controllerSocketOptions(socket)
	if err != nil {
		return localRunRuntime{}, err
	}
	controller, err := client.NewEngineClient(transport)
	if err != nil {
		cleanup()
		return localRunRuntime{}, err
	}
	return localRunRuntime{
		controller: controller,
		follow: func(ctx context.Context, run, format string, out io.Writer) error {
			return followControllerRunLogs(ctx, transport, run, format, out)
		},
		ensureController: func(ctx context.Context) error {
			_, err := ensureLocalController(ctx, home, func(ctx context.Context) (dashboardServices, error) {
				return resolveLocalServicesAt(ctx, home)
			})
			return err
		},
		close: func() error { cleanup(); return nil },
		prepare: func(ctx context.Context, pipeline string, flags runFlags) (localRunPreparation, error) {
			return prepareNativeLocalRun(ctx, home, directory, pipeline, captured, snapshots)
		},
		bind: func(ctx context.Context, award controllerwire.ClaimedUnit) (hereHostSession, error) {
			return bindLocalRunHost(ctx, home, transport, award)
		},
	}, nil
}

func hostSourceOptions(home string, captured []string, masker *secrets.Masker, local bool) (source.Options, error) {
	mode, err := toolchain.ParseMode(os.Getenv(toolchainModeEnv))
	if err != nil {
		return source.Options{}, err
	}
	engines, err := engine.NewResolver(engine.Config{BuildVersion: installedVersion(), Mode: mode, CacheDirectory: filepath.Join(home, "engines"), LocalOnly: local})
	if err != nil {
		return source.Options{}, err
	}
	return source.Options{
		Engines: engines, SDK: engine.GoSDK,
		Target:    releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		Workspace: filepath.Join(home, "workspaces"), Base: procenv.Base{Local: local, Captured: captured, Ambient: os.Environ(), SubmitEnvAllow: []string{"GOCACHE", "GOMODCACHE"}, Masker: masker},
	}, nil
}

func prepareNativeLocalRun(ctx context.Context, home, directory, pipeline string, captured []string, snapshots *nodehost.FileEnvironmentSnapshots) (_ localRunPreparation, err error) {
	route, err := loadRunRouting(directory, pipeline)
	if err != nil {
		return localRunPreparation{}, err
	}
	masker := secrets.NewMasker()
	environment := slices.DeleteFunc(slices.Clone(captured), func(item string) bool {
		name, value, _ := strings.Cut(item, "=")
		if !slices.Contains(route.LocalSecrets, name) {
			return false
		}
		masker.Register(value)
		return true
	})
	defer func() {
		if err != nil {
			err = &maskedCLIError{err: err, masker: masker}
		}
	}()
	seal, err := snapshots.Capture(ctx, environment)
	if err != nil {
		return localRunPreparation{}, err
	}
	identity, err := pipelineIdentityAt(directory, pipeline)
	if err != nil {
		return localRunPreparation{}, err
	}
	configFile, err := userconfig.Path()
	if err != nil {
		return localRunPreparation{}, err
	}
	machine, err := userconfig.ReadConfig(configFile)
	if err != nil && !os.IsNotExist(err) {
		return localRunPreparation{}, err
	}
	options, err := hostSourceOptions(home, environment, masker, true)
	if err != nil {
		return localRunPreparation{}, err
	}
	if typescript, err := pipelineentry.IsTypeScript(directory); err != nil {
		return localRunPreparation{}, err
	} else if typescript {
		options.SDK = engine.TypeScriptSDK
	}
	root := filepath.Dir(directory)
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: root})
	if err != nil {
		return localRunPreparation{}, err
	}
	entry, err := source.Acquire(ctx, source.Spec{RepoKey: string(repository.Key), LocalCheckout: root, PipelineDir: filepath.Base(directory)}, options)
	if entry != nil {
		defer func() { err = errors.Join(err, entry.Close()) }()
	}
	if err != nil {
		return localRunPreparation{}, err
	}
	if entry == nil {
		return localRunPreparation{}, errors.New("local source acquisition returned no entry")
	}
	if err := writeEngineNotices(os.Stderr, entry.Engine.Notices); err != nil {
		return localRunPreparation{}, err
	}
	processes := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base, Snapshots: snapshots}}
	planner := nodehost.SDKPlanner{
		Start: processes.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: masker,
		Log: func(ctx context.Context, frame nodeproto.FrameLog) error {
			if err := context.Cause(ctx); err != nil {
				return err
			}
			_, err := fmt.Fprintln(os.Stderr, frame.Message)
			return err
		},
	}
	prepared, err := planner.Prepare(ctx, nodehost.PlanningProcessInput{
		Entry: entry, Entrypoint: identity.Entrypoint,
		Environment: seal, Args: map[string]string{}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
		Run: nodeproto.RunContext{RunID: rand.Text(), Pipeline: pipeline},
	}, nodehost.PlanningInput{})
	if err != nil {
		return localRunPreparation{}, err
	}
	localSecrets, err := resolveBurstSecrets(ctx, route.LocalSecrets, prepared.Description.Secrets, captured)
	if err != nil {
		return localRunPreparation{}, err
	}
	return localRunPreparation{
		identity: identity, described: []nodeproto.PipelineDescription{prepared.Description},
		git:         runspec.Git{Repository: repository, SHA: entry.SHA},
		source:      runspec.Source{Ref: entry.SHA, Entrypoint: identity.Entrypoint, Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: filepath.Base(directory)}},
		environment: seal, localSecrets: localSecrets, machine: machine, machineFile: configFile,
		settings: settings.Inputs{Context: settings.Context{Origin: runspec.OriginLocal, Submitter: true, Local: true, NumCPU: runtime.NumCPU()}},
	}, nil
}
