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
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// productionRuntime composes installed commands with authenticated controller and native host services.
func productionRuntime(version string) clusterRuntime {
	return clusterRuntime{
		Launcher: productionLauncher(version),
		Runner: RunnerCLI{Actions: func(ctx context.Context, controller, team string) (capability.Issued, error) {
			return client.ExchangeGitHubActions(ctx, client.EngineOptions{URL: controller}, team, os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"))
		}, Factory: func(ctx context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
			home, err := wingd.HomeDir("")
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			engines, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: filepath.Join(home, "engines")})
			if err != nil {
				return runners.RuntimeConfig{}, err
			}

			hosting := &productionHost{UnitHost: productionHostingAt(home, version, client.EngineOptions{URL: *options.Config.Controller}, "", "", options.Config.LocalAdmission != nil && *options.Config.LocalAdmission, options.SubmitAccept, options.actions)}
			installation, err := productionInstallation(options.ConfigPath, hosting)
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			return runners.RuntimeConfig{
				Installation: installation, Versions: engines, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
				Hosting: hosting,
			}, nil
		}},
		Bootstrap: BootstrapRuntime{
			Source: func(_ context.Context, options BootstrapOptions) (launcher.SourceInit, error) {
				return launcher.SourceInit{Bind: func(_ context.Context, award launcher.CloudJobAward) (source.Options, func() error, error) {
					access, err := client.NewSourceAccess(client.EngineOptions{URL: options.ControllerURL, Bearer: fixedBearer(string(award.Bootstrap))}, string(award.Binding.RunID), award.Source)
					return source.Options{Access: access, Base: procenv.Base{Ambient: os.Environ(), Masker: secrets.NewMasker()}}, nil, err
				}}, nil
			},
			Host: func(ctx context.Context, options BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
				if err := waitForDocker(ctx, os.Getenv("DOCKER_HOST"), dockerStartup); err != nil {
					return launcher.BootstrapHost{}, nil, err
				}
				home, err := wingd.HomeDir("")
				if err != nil {
					return launcher.BootstrapHost{}, nil, err
				}
				return launcher.BootstrapHost{Host: productionHostingAt(home, version, client.EngineOptions{URL: options.ControllerURL}, options.SourceDir, options.ModuleCache, false, procenv.SubmitAcceptAll(), nil)}, nil, nil
			},
		},
	}
}

func fixedBearer(raw string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return raw, context.Cause(ctx) }
}

// safety: a Cloud Job serves one claim and is then discarded, and a requester's own machine is its operator, so both accept every submittable name.
func productionHostingAt(home, version string, connection client.EngineOptions, checkout, modules string, admission bool, accept procenv.SubmitAccept, actions *githubActionsSource) runners.Hosting {
	return runners.Hosting{ControllerURL: connection.URL, HTTP: connection.HTTP, Bind: func(ctx context.Context, unit controllerwire.ClaimedUnit, controller controllerwire.Controller) (services nodehost.NativeServices, principal capability.Principal, err error) {
		options := connection
		options.Bearer, options.Launcher = fixedBearer(unit.Capability), false
		var private *client.LocalHostClient
		if unit.Unit == capability.UnitNode {
			private, err = client.NewNodeHostClient(options, unit.RunID, unit.JobID)
		} else {
			private, err = client.NewLocalHostClient(options, unit.RunID)
		}
		if err != nil {
			return services, principal, err
		}
		closeResources := private.Close
		defer func() {
			if err != nil {
				err = errors.Join(err, closeResources())
			}
		}()
		refresh := func(ctx context.Context) (capability.Principal, error) {
			p, e := private.Principal(ctx)
			if e == nil {
				e = runners.ValidateAwardPrincipal(unit, p)
			}
			return p, e
		}
		principal, err = refresh(ctx)
		if err != nil {
			return services, principal, err
		}
		var processes *nodehost.NativeProcesses
		state, err := nodehost.NewRemoteHostStore(controller, private, unit.UnitID, func() bool { return processes != nil && processes.Computing(unit.RunID) })
		if err != nil {
			return services, principal, err
		}
		state.Finish = private.FinishRunAndUnit
		engines, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: filepath.Join(home, "engines")})
		if err != nil {
			return services, principal, err
		}
		input, err := nodehost.NewRunInput(unit)
		if err != nil {
			return services, principal, err
		}
		spec, err := source.ForRequest(unit.Request)
		if input.Accepted != nil {
			spec, err = source.ForExecution(unit.Accepted.Spec)
		}
		if err != nil {
			return services, principal, err
		}
		access, err := client.NewSourceAccess(options, unit.RunID, spec)
		if err != nil {
			return services, principal, err
		}
		if actions != nil {
			access = actions.bind(access)
		}
		submitted := accept
		if unit.OwnerMachine {
			submitted = procenv.SubmitAcceptAll()
		}
		var machineAdmission *nodehost.LocalAdmission
		if admission {
			machineAdmission = nodehost.MachineAdmission(home, version)
			machineAdmission.Controller = connection.URL
		}
		services, err = nodehost.NewNativeServices(ctx, nodehost.WithClient(nodehost.NativeConfig{
			Admission:    nodehost.ComposeStartGate(state, machineAdmission, false),
			SubmittedEnv: unit.SubmittedEnv, SubmitAccept: submitted,
			Home: home, Store: state, Parent: private, Bindings: private, Principal: principal, Refresh: refresh, Local: store.OwnerCanExecute(principal),
			Source: source.Options{PreparedCheckout: checkout, Engines: engines, Access: access, SDK: engineid.GoSDK, Workspace: filepath.Join(home, "workspaces"), Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, GitAccess: ownerGitAccess(unit.OwnerMachine), Base: runnerInstallBase(unit.OwnerMachine, modules)},
			Renew: func(ctx context.Context, _ string, computing bool) (runstate.LeaseBeat, error) {
				return state.RenewOwner(ctx, runstate.Actor{}, computing)
			},
		}, private))
		if err != nil {
			return services, principal, err
		}

		processes = services.Config.Processes
		closeResources = func() error { return errors.Join(services.Close(), private.Close()) }
		originalClose := services.Close
		services.Close = func() error { return errors.Join(originalClose(), private.Close()) }
		if checkout != "" {
			docker, e := dockerClient(os.Getenv("DOCKER_HOST"))
			if e != nil {
				return services, principal, e
			}
			services.Config.Lease.ExecutionEnded = func() {
				kill, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				err := stopDocker(kill, docker)
				if err != nil {
					slog.Error("stop Cloud Docker execution", "err", err)
				}
			}
		}
		if unit.Unit == capability.UnitTrigger {
			adopt := services.Config.Owner.AdoptClaim
			services.Config.Owner.AdoptClaim = func(ctx context.Context, claim runstate.ReconciledNode) error {
				if err := private.AdoptClaim(ctx, claim); err != nil {
					return err
				}
				return adopt(ctx, claim)
			}
		}
		return services, principal, nil
	}}
}

func ownerGitAccess(owner bool) source.GitAccess {
	if owner {
		return source.GitAccessOwner
	}
	return source.GitAccessTeam
}

// perf: compiles keep the runner's GOCACHE and GOMODCACHE, as a local run does, so the cache volume stays warm.
func runnerInstallBase(owner bool, modules string) procenv.Base {
	return procenv.Base{OwnerMachine: owner, Ambient: runnerToolEnvironment(modules), SubmitEnvAllow: []string{"GOCACHE", "GOMODCACHE"}, Masker: secrets.NewMasker()}
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
