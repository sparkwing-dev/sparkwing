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
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
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
		openCache:  openLocalProfileCache,
		publish: func(ctx context.Context, profile, run string) error {
			return publishLocalProfile(ctx, home, transport, profile, run)
		},
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
			return prepareNativeLocalRun(ctx, home, directory, pipeline, captured, snapshots, flags)
		},
		bind: func(ctx context.Context, award controllerwire.ClaimedUnit) (hereHostSession, error) {
			return bindLocalRunHost(ctx, home, transport, award)
		},
	}, nil
}

func openLocalProfileCache(ctx context.Context, profile string, request *runspec.Request) (blob.RemoteCache, func() error, error) {
	selected, err := prepareTriggerSource(ctx, triggerSourceInput{checkout: request.Source.Location.LocalCheckout, pipeline: request.Pipeline, environment: os.Environ(), githubID: func(ctx context.Context, remote string) (int64, error) {
		return lookupTriggerGitHubID(ctx, remote, os.Environ())
	}})
	if err != nil {
		return nil, nil, err
	}
	cache, closeCache, err := openProfileCache(ctx, profile, selected.git, request.Source.Location.LocalCheckout)
	if err == nil {
		request.Git.Repository, request.Git.Ref, request.Git.DefaultBranch = selected.git.Repository, selected.git.Ref, selected.git.DefaultBranch
	}
	return cache, closeCache, err
}

func openProfileCache(ctx context.Context, profile string, git runspec.Git, checkout string) (blob.RemoteCache, func() error, error) {
	branch, err := checkoutDefaultBranch(ctx, checkout)
	if err != nil {
		return nil, nil, err
	}
	if branch == "" {
		return nil, nil, errors.New("run: cache profile requires origin's default branch")
	}
	options, closeConnection, err := controllerOptions(ctx, profile)
	if err != nil {
		return nil, nil, err
	}
	credential, err := client.OpenProfileData(ctx, options, capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: string(git.Repository.Key), ReadRefs: []string{branch}, VerifiedDefaultOnly: true})
	if err != nil {
		closeConnection()
		return nil, nil, err
	}
	closeCache := func() error {
		defer closeConnection()
		return credential.Close(context.WithoutCancel(ctx))
	}
	cache, err := credential.RemoteCache(profile)
	if err != nil {
		return nil, nil, errors.Join(err, closeCache())
	}
	return cache, closeCache, nil
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

func prepareNativeLocalRun(ctx context.Context, home, directory, pipeline string, captured []string, snapshots *nodehost.FileEnvironmentSnapshots, flags runFlags) (_ localRunPreparation, err error) {
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
	} else if err := resolveSparks(ctx, directory, compileOptions{NoUpdate: flags.noUpdate}); err != nil {
		return localRunPreparation{}, err
	}
	root := filepath.Dir(directory)
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: root})
	if err != nil {
		return localRunPreparation{}, err
	}
	entry, err := source.Acquire(ctx, source.Spec{RepoKey: string(repository.Key), LocalCheckout: root, PipelineDir: filepath.Base(directory), NoBincache: flags.noBincache, HashAllFiles: flags.hashAllFiles}, options)
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
		source:      runspec.Source{Ref: entry.SHA, Entrypoint: identity.Entrypoint, Location: runspec.SourceLocation{LocalCheckout: root, PipelineDir: filepath.Base(directory)}, NoBincache: flags.noBincache, HashAllFiles: flags.hashAllFiles},
		environment: seal, localSecrets: localSecrets, machine: machine, machineFile: configFile,
		settings: settings.Inputs{Context: settings.Context{Origin: runspec.OriginLocal, Submitter: true, Local: true, NumCPU: runtime.NumCPU()}},
	}, nil
}
