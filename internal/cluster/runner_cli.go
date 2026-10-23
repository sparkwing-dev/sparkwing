package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

// RunnerOptions carries machine settings to the shared runtime composition.
type RunnerOptions struct {
	actions          *githubActionsSource
	Config           userconfig.RunnerConfig
	ConfigPath       string
	BoxID            *string
	SubmitAccept     procenv.SubmitAccept
	Loop             runners.LoopConfig
	Heartbeat, Lease time.Duration
	WarmModules      []string
	MetricsAddress   string
	CPUCeiling       *float64
	MemoryCeiling    *int64
	Deadline         *time.Duration
	Bearer           func(context.Context) (string, error) `json:"-"`
	Output, Checkout string
}

// RunnerFactory supplies host, policy and installation services to the shared constructor.
type RunnerFactory func(context.Context, RunnerOptions) (runners.RuntimeConfig, error)

// RunnerCLI shares the start and warm runtime. ReadConfig must implement the
// complete userconfig.ReadConfig validation contract before returning a section.
// Actions must return a server-issued push-bound capability, never App trust.
type RunnerCLI struct {
	ReadConfig func(string) (userconfig.Config, error)
	Factory    RunnerFactory
	Actions    func(context.Context, string, string) (capability.Issued, error)
}

// Run parses the final grammar before reading credentials or constructing a host.
func (cli RunnerCLI) Run(ctx context.Context, command string, args []string, version string) error {
	if command != "start" && command != "warm" {
		return fmt.Errorf("unknown subcommand %q", command)
	}
	fs, err := commandFlags(command, args)
	if err != nil {
		return parsedHelp(err)
	}
	config, accepted, boxID, err := cli.config(command, fs)
	if err != nil {
		return err
	}
	options, err := runnerOptions(config, fs, version)
	if err != nil {
		return err
	}
	options.BoxID = boxID
	if options.SubmitAccept, err = procenv.OperatorSubmitAccept(accepted); err != nil {
		return err
	}
	if command == "start" {
		options.ConfigPath = textFlag(fs, "config")
		if options.ConfigPath == "" {
			options.ConfigPath, err = userconfig.Path()
			if err != nil {
				return err
			}
		}
	}
	if enabled(fs, "github-actions") {
		if err := cli.authorizeActions(ctx, fs, &options); err != nil {
			return err
		}
	}
	if cli.Factory == nil {
		return errors.New("runner runtime is unavailable: compose the capability controller, host library and signed update resolver")
	}
	if options.Bearer == nil {
		directory := ""
		if options.Config.CredentialsDir != nil {
			directory = *options.Config.CredentialsDir
		}
		options.Bearer, err = runners.FileBearer(directory)
		if err != nil {
			return fmt.Errorf("runner credential: %w", err)
		}
	}
	repositories, err := sourceurl.ParseRepoAllowlist(options.Config.Repos)
	if err != nil {
		return err
	}
	transport := client.EngineOptions{URL: *options.Config.Controller, Bearer: options.Bearer}
	loop := options.Loop
	loop.Kinds, loop.Labels = slices.Clone(loop.Kinds), slices.Clone(loop.Labels)
	runtimeConfig, err := cli.Factory(ctx, options)
	if err != nil {
		return err
	}
	transport.HTTP = runtimeConfig.Controller.HTTP
	runtimeConfig.Controller, runtimeConfig.Loop, runtimeConfig.Repositories = transport, loop, repositories
	if runtimeConfig.OnError == nil {
		runtimeConfig.OnError = func(err error) { slog.Error("runner unit failed", "err", err) }
	}
	runner, err := runners.NewManagedRunner(ctx, runtimeConfig)
	if err != nil {
		return err
	}
	return runRunner(ctx, runner, options.MetricsAddress)
}

func (cli RunnerCLI) config(command string, fs *flag.FlagSet) (cfg userconfig.RunnerConfig, accepted []string, boxID *string, err error) {
	if command == "start" {
		read := cli.ReadConfig
		if read == nil {
			read = userconfig.ReadConfig
		}
		path := textFlag(fs, "config")
		if fs.Changed("config") && path == "" {
			return cfg, nil, nil, errors.New("--config must not be empty")
		}
		if path == "" {
			path, err = userconfig.Path()
			if err != nil {
				return cfg, nil, nil, err
			}
		}
		file, err := read(path)
		if err != nil {
			return cfg, nil, nil, err
		}
		if file.Runner == nil {
			return cfg, nil, nil, errors.New("config requires a runner section")
		}
		cfg = *file.Runner
		if file.Machine != nil {
			boxID = file.Machine.BoxID
		}
		if file.Run != nil {
			accepted = file.Run.SubmitEnvAllow
		}
	}
	stringFields := map[string]**string{"controller": &cfg.Controller, "credentials-dir": &cfg.CredentialsDir, "holder-prefix": &cfg.HolderPrefix, "poll": &cfg.Poll, "heartbeat": &cfg.Heartbeat, "lease": &cfg.Lease}
	for name, target := range stringFields {
		if fs.Changed(name) {
			value := textFlag(fs, name)
			*target = &value
		}
	}
	for name, target := range map[string]**bool{"accept-working-tree": &cfg.AcceptWorkingTree, "local-admission": &cfg.LocalAdmission} {
		if fs.Changed(name) {
			value := enabled(fs, name)
			*target = &value
		}
	}
	for name, target := range map[string]*[]string{"repos": &cfg.Repos, "label": &cfg.Labels, "submit-env-allow": &accepted} {
		if fs.Changed(name) {
			*target, _ = fs.GetStringSlice(name)
		}
	}
	if fs.Changed("max-concurrent") {
		value, err := strconv.Atoi(textFlag(fs, "max-concurrent"))
		if err != nil {
			return cfg, nil, nil, errors.New("--max-concurrent requires an integer")
		}
		cfg.MaxConcurrent = &value
	}
	return cfg, accepted, boxID, nil
}

func runnerOptions(cfg userconfig.RunnerConfig, fs *flag.FlagSet, version string) (RunnerOptions, error) {
	options := RunnerOptions{Config: cfg, Loop: runners.LoopConfig{Version: version, Labels: slices.Clone(cfg.Labels), MaxConcurrent: limits.RunnerConcurrency}, Output: textFlag(fs, "output"), Checkout: textFlag(fs, "checkout")}
	if cfg.Controller == nil || strings.TrimSpace(*cfg.Controller) == "" {
		return options, errors.New("runner controller is required")
	}
	if err := deployment.CheckControllerURL("runner controller", *cfg.Controller); err != nil {
		return options, err
	}
	if cfg.MaxConcurrent != nil {
		options.Loop.MaxConcurrent = *cfg.MaxConcurrent
	}
	if fs.Changed("holder-prefix") {
		return options, errors.New("runner holder-prefix is unavailable: the controller claim contract owns holder identity")
	}
	if options.Loop.MaxConcurrent < 1 {
		return options, errors.New("runner max-concurrent must be positive")
	}
	if _, err := sourceurl.ParseRepoAllowlist(cfg.Repos); err != nil {
		return options, fmt.Errorf("runner repos: %w", err)
	}
	var err error
	options.Loop.PollInterval, err = runnerDuration("poll", cfg.Poll, limits.ClaimPoll)
	if err != nil {
		return options, err
	}
	options.Lease, err = runnerDuration("lease", cfg.Lease, limits.DefaultLease)
	if err != nil {
		return options, err
	}
	options.Heartbeat, err = runnerDuration("heartbeat", cfg.Heartbeat, limits.Renew(options.Lease))
	if err != nil {
		return options, err
	}
	if options.Loop.PollInterval <= 0 {
		return options, errors.New("runner poll must be positive")
	}
	if err := limits.CheckRenewal(options.Heartbeat, options.Lease); err != nil {
		return options, err
	}
	if fs.Changed("idle-exit") {
		value := textFlag(fs, "idle-exit")
		options.Loop.IdleExit, err = runnerDuration("idle-exit", &value, 0)
		if err != nil {
			return options, err
		}
		if options.Loop.IdleExit < 0 {
			return options, errors.New("runner idle-exit must not be negative")
		}
	}
	return warmOptions(options, fs)
}

func warmOptions(options RunnerOptions, fs *flag.FlagSet) (RunnerOptions, error) {
	options.MetricsAddress = textFlag(fs, "metrics-addr")
	if err := runnerCeilings(&options, fs); err != nil {
		return options, err
	}
	sources, _ := fs.GetStringSlice("trigger-source")
	if len(sources) > 0 {
		return options, errors.New("--trigger-source is unavailable: the controller claim contract has no source filter")
	}
	modules, _ := fs.GetStringSlice("warm-module")
	var err error
	options.WarmModules, err = parseWarmModules(strings.Join(modules, ","), options.Loop.Version)
	if err != nil {
		return options, fmt.Errorf("--warm-module: %w", err)
	}
	if fs.Changed("max-claims-before-restart") {
		value, err := strconv.Atoi(textFlag(fs, "max-claims-before-restart"))
		if err != nil || value < 0 {
			return options, errors.New("--max-claims-before-restart must be a nonnegative integer")
		}
		options.Loop.MaxClaims = value
	}
	claimJobs, claimTriggers := true, true
	if fs.Changed("claim-jobs") {
		claimJobs = enabled(fs, "claim-jobs")
	}
	if fs.Changed("also-claim-triggers") {
		claimTriggers = enabled(fs, "also-claim-triggers")
	}
	if !claimJobs && !claimTriggers {
		return options, errors.New("runner must claim jobs, triggers or both")
	}
	if claimTriggers {
		options.Loop.Kinds = append(options.Loop.Kinds, capability.UnitTrigger, capability.UnitPlan)
	}
	if claimJobs {
		options.Loop.Kinds = append(options.Loop.Kinds, capability.UnitEval, capability.UnitNode)
	}
	if fs.Changed("deadline") {
		value, err := time.ParseDuration(textFlag(fs, "deadline"))
		if err != nil || value <= 0 {
			return options, errors.New("--deadline must be positive")
		}
		options.Deadline = &value
	}
	return options, nil
}

func runnerCeilings(options *RunnerOptions, fs *flag.FlagSet) error {
	for _, name := range []string{"cpu-ceiling", "memory-ceiling"} {
		if !fs.Changed(name) {
			continue
		}
		quantity, err := resource.ParseQuantity(textFlag(fs, name))
		if err != nil || quantity.Sign() <= 0 {
			return fmt.Errorf("--%s requires a positive quantity", name)
		}
		if name == "cpu-ceiling" {
			value := quantity.AsApproximateFloat64()
			if math.IsInf(value, 0) || math.IsNaN(value) {
				return errors.New("--cpu-ceiling must be finite")
			}
			options.CPUCeiling = &value
		} else {
			if quantity.Cmp(*resource.NewQuantity(math.MaxInt64, resource.DecimalSI)) > 0 {
				return errors.New("--memory-ceiling must fit in a signed 64-bit byte count")
			}
			value := quantity.Value()
			options.MemoryCeiling = &value
		}
	}
	return nil
}

func runnerDuration(name string, value *string, fallback time.Duration) (time.Duration, error) {
	if value == nil {
		return fallback, nil
	}
	duration, err := time.ParseDuration(*value)
	if err != nil {
		return 0, fmt.Errorf("runner %s: %w", name, err)
	}
	return duration, nil
}

func (cli RunnerCLI) authorizeActions(ctx context.Context, fs *flag.FlagSet, options *RunnerOptions) error {
	team := textFlag(fs, "team")
	if team == "" {
		return errors.New("--github-actions requires --team")
	}
	if cli.Actions == nil {
		return errors.New("GitHub Actions exchange is unavailable: compose the controller capability exchange")
	}
	issued, err := cli.Actions(ctx, *options.Config.Controller, team)
	if err != nil {
		return err
	}
	push, expires := issued.Capability.Binding.Push, issued.Capability.ExpiresAt
	if issued.Raw == "" || issued.Capability.Team != team || push == nil || push.RepositoryKey == "" || push.Ref == "" || push.SHA == "" || push.WorkflowRunID == "" || expires == nil || !expires.After(time.Now()) {
		return errors.New("GitHub Actions exchange returned no live push-bound capability")
	}
	options.Bearer = func(ctx context.Context) (string, error) {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		return issued.Raw, nil
	}
	options.Loop.ClaimUntil = *expires
	options.actions = &githubActionsSource{repository: push.RepositoryKey, token: os.Getenv("GITHUB_TOKEN")}
	if options.Config.Repos == nil {
		options.Config.Repos = []string{push.RepositoryKey}
	}
	if !slices.Contains(options.Loop.Labels, "github-actions") {
		options.Loop.Labels = append(options.Loop.Labels, "github-actions")
	}
	return nil
}
