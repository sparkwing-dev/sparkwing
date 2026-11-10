package cluster

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// productionRuntime composes installed commands with authenticated controller and native host services.
func productionRuntime(version string) clusterRuntime {
	return clusterRuntime{
		Launcher: productionLauncher(version),
		Runner: RunnerCLI{Actions: func(ctx context.Context, controller, team, receipt string) (capability.Issued, error) {
			return client.ExchangeGitHubActions(ctx, client.EngineOptions{URL: controller}, team, os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"), receipt)
		}, Factory: func(ctx context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
			home, err := wingd.HomeDir("")
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			engines, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: filepath.Join(home, "engines"), LocalBuildDirectory: filepath.Join(home, "runners", "builds")})
			if err != nil {
				return runners.RuntimeConfig{}, err
			}

			hosting := &productionHost{UnitHost: productionHostingAt(home, version, client.EngineOptions{URL: *options.Config.Controller}, "", options.Config.LocalAdmission != nil && *options.Config.LocalAdmission, options.SubmitAccept, options.actions)}
			installation, err := productionInstallation(options.ConfigPath, hosting)
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			var open func(client.EngineOptions) (controllerwire.Controller, error)
			if options.Config.LocalAdmission != nil && *options.Config.LocalAdmission {
				open = nativeCapacityClient(home, version)
			}
			return runners.RuntimeConfig{
				NewController: open,
				Installation:  installation, Versions: engines, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
				Hosting: hosting,
			}, nil
		}},
		Bootstrap: BootstrapRuntime{
			Host: func(ctx context.Context, options BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
				// safety: admission keeps the launcher's environment out of every launcher job, Cloud or team, so a job
				// has no development override and a development job image always refuses work.
				if err := engine.RequirePolicyBuild("Cloud job", version, "", false); err != nil {
					return launcher.BootstrapHost{}, nil, err
				}
				// perf: the copy overlaps the Docker sidecar's startup.
				seeded := make(chan error, 1)
				go func() {
					seeded <- seedGoCaches(goSeedRoot, goStandardSeed, version, options.ModuleCache, os.Getenv("GOCACHE"))
				}()
				if err := waitForDocker(ctx, os.Getenv("DOCKER_HOST"), dockerStartup); err != nil {
					return launcher.BootstrapHost{}, nil, err
				}
				if err := <-seeded; err != nil {
					slog.Warn("Go caches start cold", "err", err)
				}
				home, err := wingd.HomeDir("")
				if err != nil {
					return launcher.BootstrapHost{}, nil, err
				}
				return launcher.BootstrapHost{Host: productionHostingAt(home, version, client.EngineOptions{URL: options.ControllerURL}, options.ModuleCache, false, procenv.SubmitAcceptAll(), nil)}, nil, nil
			},
		},
	}
}

func fixedBearer(raw string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return raw, context.Cause(ctx) }
}

// safety: a Cloud Job serves one claim and is then discarded, and a requester's own machine is its operator, so both accept every submittable name.
func productionHostingAt(home, version string, connection client.EngineOptions, modules string, admission bool, accept procenv.SubmitAccept, actions *githubActionsSource) runners.Hosting {
	var machineAdmission *nodehost.LocalAdmission
	if admission {
		machineAdmission = nodehost.RunnerMachineAdmission(home, version)
		machineAdmission.Controller = connection.URL
	}
	hosting := runners.NativeHosting(runners.NativeHostingConfig{
		Home: home, Controller: connection, Admission: machineAdmission, SubmitAccept: accept, PrepareSource: modules != "",
		Source: func(ctx context.Context, unit controllerwire.ClaimedUnit, options client.EngineOptions) (source.Options, error) {
			engines, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: filepath.Join(home, "engines"), LocalBuildDirectory: filepath.Join(home, "runners", "builds")})
			if err != nil {
				return source.Options{}, err
			}
			spec, err := source.ForRequest(unit.Request)
			if unit.Accepted != nil {
				spec, err = source.ForExecution(unit.Accepted.Spec)
			}
			if err != nil {
				return source.Options{}, err
			}
			access, err := client.NewSourceAccess(options, unit.RunID, spec)
			if err != nil {
				return source.Options{}, err
			}
			if actions != nil {
				access = actions.bind(access)
			}
			return source.Options{Engines: engines, Access: access, SDK: engineid.GoSDK, Workspace: filepath.Join(home, "workspaces"), Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, GitAccess: ownerGitAccess(unit.OwnerMachine), Base: runnerInstallBase(unit.OwnerMachine, modules)}, nil
		},
	})
	if modules == "" {
		return hosting
	}
	bind := hosting.Bind
	hosting.Bind = func(ctx context.Context, unit controllerwire.ClaimedUnit, controller controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error) {
		services, principal, err := bind(ctx, unit, controller)
		if err != nil {
			return services, principal, err
		}
		docker, err := dockerClient(os.Getenv("DOCKER_HOST"))
		if err != nil {
			return services, principal, err
		}
		services.Config.Lease.ExecutionEnded = func() {
			kill, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if err := stopDocker(kill, docker); err != nil {
				slog.Error("stop Cloud Docker execution", "err", err)
			}
		}
		return services, principal, nil
	}
	return hosting
}

func ownerGitAccess(owner bool) source.GitAccess {
	if owner {
		return source.GitAccessOwner
	}
	return source.GitAccessTeam
}

// perf: compiles keep the runner's GOCACHE and GOMODCACHE, as a local run does, so the cache volume stays warm.
func runnerInstallBase(owner bool, modules string) procenv.Base {
	allow := []string{"GOCACHE", "GOMODCACHE"}
	if modules != "" {
		allow = append(allow, "DOCKER_HOST")
	}
	return procenv.Base{OwnerMachine: owner, Ambient: runnerToolEnvironment(modules), SubmitEnvAllow: allow, Masker: secrets.NewMasker()}
}

func runnerToolEnvironment(modules string) []string {
	environment := os.Environ()
	if modules == "" {
		return environment
	}
	for i := 0; i < len(environment); i++ {
		if strings.HasPrefix(environment[i], "GOMODCACHE=") {
			environment = append(environment[:i], environment[i+1:]...)
			i--
		}
	}
	return append(environment, "GOMODCACHE="+modules)
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

type githubActionsSource struct {
	source.Access
	repository, token string
}

func (s *githubActionsSource) bind(access source.Access) source.Access {
	return &githubActionsSource{Access: access, repository: s.repository, token: s.token}
}

func (s *githubActionsSource) GitCredential(ctx context.Context, spec source.Spec) (source.Credential, error) {
	if err := context.Cause(ctx); err != nil {
		return source.Credential{}, err
	}
	host, err := sourceurl.Host(spec.RemoteURL)
	if spec.RepoKey != s.repository || err != nil || host != "github.com" || s.token == "" {
		return source.Credential{}, errors.New("Actions source requires this repository's GITHUB_TOKEN")
	}
	return source.Credential{Kind: "https", Host: "github.com", Username: "x-access-token", Secret: s.token}, nil
}
