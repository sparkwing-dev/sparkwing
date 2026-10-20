package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func runNativeBurst(ctx context.Context, pipeline string, flags runFlags, args []string, route userconfig.RunRouting, connection client.EngineOptions) error {
	handlePath := ""
	if flags.runHandleFile != "" {
		var err error
		handlePath, err = filepath.Abs(flags.runHandleFile)
		if err != nil {
			return err
		}
		release, err := orchestrator.ReserveRunHandle(handlePath)
		if err != nil {
			return err
		}
		defer release()
	}
	preparing, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	home, err := wingd.HomeDir("")
	if err != nil {
		return err
	}
	if _, err := ensureLocalController(preparing, home, func(ctx context.Context) (dashboardServices, error) { return resolveLocalServicesAt(ctx, home) }); err != nil {
		return err
	}
	request, err := prepareNativeBurst(preparing, pipeline, flags, args, route, connection)
	if err != nil {
		return err
	}
	controller, err := client.NewEngineClient(connection)
	if err != nil {
		return err
	}
	return submitTriggeredRun(preparing, request, false, flags.outputFormat, os.Stdout, triggerSubmission{
		Controller: controller,
		here: func(_ context.Context, response controllerwire.CreateRunResponse) error {
			interrupted := preparing.Err()
			stop()
			var cancelErr error
			if interrupted != nil {
				_, cancelErr = controller.Cancel(ctx, response.RunID, controllerwire.CancelRequest{Reason: "interrupted by here host"})
			}
			deps := defaultHereDependencies(flags.outputFormat)
			if handlePath != "" {
				deps.admitted = func(response controllerwire.CreateRunResponse) error {
					return orchestrator.PublishRunHandle(handlePath, orchestrator.NewRunHandle(response.RunID, pipeline, "pending"))
				}
			}
			return errors.Join(cancelErr, handoffHereRun(ctx, connection, response, deps))
		},
	})
}

func prepareNativeBurst(ctx context.Context, pipeline string, flags runFlags, args []string, route userconfig.RunRouting, connection client.EngineOptions) (_ controllerwire.CreateRunRequest, resultErr error) {
	environment := flags.environment
	if environment == nil {
		environment = prepareLocalEnvironment(os.Environ(), route.LocalSecrets)
	}
	captured, held, masker := environment.captured, environment.held, environment.masker
	configPath, err := userconfig.Path()
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	machine, err := userconfig.ReadConfig(configPath)
	if err != nil && !os.IsNotExist(err) {
		return controllerwire.CreateRunRequest{}, err
	}
	var allow []string
	if machine.Run != nil {
		allow = machine.Run.SubmitEnvAllow
	}
	submitted, err := procenv.Submit(captured, allow, masker)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	directory, err := findSparkwingDir()
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	cfg, err := projectconfig.Load(filepath.Join(directory, projectconfig.Filename))
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	var files []string
	if cfg != nil {
		for _, p := range cfg.Pipelines {
			if p.Name == pipeline {
				files = p.SourceFiles
				break
			}
		}
	}
	snapshot, err := burstSourceSnapshot(ctx, captured, filepath.Dir(directory), files, route.SourceFiles)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, snapshot.close()) }()
	selected, err := prepareTriggerSource(ctx, triggerSourceInput{checkout: snapshot.RepoRoot, snapshot: snapshot, pipeline: pipeline, environment: captured, githubID: func(ctx context.Context, remote string) (int64, error) {
		return lookupTriggerGitHubID(ctx, remote, captured)
	}})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if err := selected.checkProfile(*route.Profile); err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	selected.git.Ref, err = snapshot.branch(ctx)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	defaultBranch, err := checkoutDefaultBranch(ctx, captured, snapshot.RepoRoot, masker)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	selected.git.DefaultBranch = runspec.Optional[string]{Set: defaultBranch != "", Value: defaultBranch}
	if selected.git.SHA != snapshot.SHA || selected.source.Ref != snapshot.SHA {
		return controllerwire.CreateRunRequest{}, errors.New("run: cloud routing requires the pipeline and execution source in the captured working tree")
	}
	home, err := wingd.HomeDir("")
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	options, err := hostSourceOptions(home, captured, masker, true)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	options.LocalBundle = snapshot.BundlePath
	options.SDK = ""
	selected.source.NoBincache, selected.source.HashAllFiles = flags.noBincache, flags.hashAllFiles
	described, err := inspectTriggerSource(ctx, selected, captured, options)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	values := localRunSettingFlags(flags)
	values.Profile.Set, values.Profile.Value = true, *route.Profile
	values.Runner.Set, values.Runner.Value = true, runspec.RunnerSelection{Kind: runspec.RunnerHere}
	inputs := settings.Inputs{Context: settings.Context{Origin: runspec.OriginTrigger, Submitter: true, NumCPU: runtime.NumCPU()}, Flags: values}
	inputs.Machine.SubmitEnvAllow.Set, inputs.Machine.SubmitEnvAllow.Value = allow != nil, allow
	inputs.Machine.SubmitEnvAllow.Origin = runspec.SettingOrigin{Layer: runspec.LayerMachine, File: configPath, Key: "run.submit_env_allow"}
	request, err := prepareTriggerRequest(triggerRequestInput{home: home, source: selected, settings: inputs, described: described, args: args, submittedEnv: submitted, idempotencyKey: flags.idempotencyKey})
	if err != nil {
		return request, err
	}
	schema, err := boundPipelineSchema(described, clisurface.PipelineIdentity{Name: pipeline, Entrypoint: selected.source.Entrypoint})
	if err != nil {
		return request, err
	}
	if err := enforceRiskGate(pipeline, risksIn(schema), runFlags{allow: request.Request.Options.AllowRisk.Value, dryRun: request.Request.Options.DryRun.Value}); err != nil {
		return request, err
	}
	request.LocalSecrets, err = resolveBurstSecrets(ctx, environment.names, schema.Secrets, held)
	if err != nil {
		return request, err
	}
	for name, value := range request.LocalSecrets {
		held[name] = value
	}
	request.Request.LocalSecretNames = slices.Sorted(maps.Keys(request.LocalSecrets))
	request.Request.Burst = &runspec.BurstPlacement{OverflowMinutes: route.OverflowMinutes}
	prefer := route.Prefer
	if prefer == nil {
		prefer = []userconfig.RoutePreference{{Target: "here", Hold: "5s"}, {Target: "machines", Hold: "5s"}, {Target: "cloud"}}
	}
	for _, p := range prefer {
		hold, _ := time.ParseDuration(p.Hold)
		request.Request.Burst.Preferences = append(request.Request.Burst.Preferences, runspec.PlacementStep{Target: p.Target, Hold: hold})
	}
	digest, err := client.UploadSourceSnapshot(ctx, connection, source.Publication{Git: request.Request.Git, Source: request.Request.Source, Path: snapshot.BundlePath, Base: procenv.Base{Local: true, Captured: captured, Masker: masker}, Secrets: held})
	if err != nil {
		return request, &burstBeforeIntakeError{err}
	}
	request.Request.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: digest}
	if mode := strings.TrimSpace(flags.priority); mode == "front" || mode == "back" {
		queue, err := client.NewQueueManager(connection)
		if err != nil {
			return request, err
		}
		rank, err := resolveRelativeRunPriority(ctx, mode, queue)
		if err != nil {
			return request, err
		}
		request.Request.Options.Priority.Value = rank
	}
	return request, nil
}

func resolveBurstSecrets(ctx context.Context, names []string, declarations []nodeproto.SecretDeclaration, held map[string]string) (map[string]string, error) {
	selected := []nodeproto.SecretDeclaration{}
	for _, name := range names {
		found := false
		for _, d := range declarations {
			if d.Name == name {
				selected = append(selected, d)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("run: local secret %s must be declared by the pipeline", name)
		}
	}
	return client.ResolveLocalSecrets(ctx, selected, func(ctx context.Context, name string) (string, bool, error) {
		value, present := held[name]
		return value, present, ctx.Err()
	})
}
