package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type triggerFlags struct {
	pipeline                         string
	profile                          string
	detach                           bool
	workingTree                      bool
	wantHelp                         bool
	allowSecretFiles                 []string
	runner                           string
	reason                           string
	passthrough                      []string
	resume, ref, pipelineRef, output string
}

func parseTriggerFlags(args []string) (triggerFlags, error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "trigger"}, args...))
	if err != nil {
		return triggerFlags{}, err
	}
	flags := triggerFlags{
		pipeline: inv.Pipeline, profile: inv.Value("profile"), detach: inv.Bool("detach"),
		workingTree: inv.Bool("working-tree"), wantHelp: inv.Help(),
		allowSecretFiles: inv.Flags["allow-secret-file"], runner: inv.Value("runner"),
		reason: inv.Value("reason"), passthrough: inv.Args, resume: inv.Value("resume"),
		ref: inv.Value("ref"), pipelineRef: inv.Value("pipeline-ref"), output: inv.Output(color.IsInteractiveStdout()),
	}
	if !flags.wantHelp && flags.resume == "" {
		if err := checkHereFlags(flags); err != nil {
			return triggerFlags{}, &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
		}
	}
	return flags, nil
}

func runPipelineTrigger(args []string) error {
	ctx, stop, _ := interruptContext()
	defer stop()
	return runTriggerCommand(ctx, args, os.Stdout, prepareNativeTrigger, controllerOptions)
}

func runTriggerCommand(ctx context.Context, args []string, out io.Writer, prepare func(context.Context, triggerFlags) (controllerwire.CreateRunRequest, error), connect func(context.Context, string) (client.EngineOptions, func(), error)) error {
	flags, err := parseTriggerFlags(args)
	if err != nil {
		return err
	}
	if flags.wantHelp {
		return writeCommandHelp(cmdPipelineTrigger, out, flags.output)
	}
	if flags.resume != "" {
		return runHereResumeCommand(ctx, flags, out, connect, client.NewEngineClient,
			func(ctx context.Context, options client.EngineOptions, run string) (nodehost.RunCompletion, error) {
				return resumeAttachedHereRun(ctx, options, run, defaultHereDependencies(flags.output))
			})
	}
	if err := refuseOtherThanPinnedProfile(flags.pipeline, flags.profile, flags.workingTree); err != nil {
		return err
	}
	request, err := prepare(ctx, flags)
	if err != nil {
		return err
	}
	options, closeTransport, err := connect(ctx, flags.profile)
	if err != nil {
		return err
	}
	defer closeTransport()
	controller, err := client.NewEngineClient(options)
	if err != nil {
		return err
	}
	return submitTriggeredRun(ctx, request, flags.detach, flags.output, out, triggerSubmission{
		Controller: controller,
		here: func(ctx context.Context, response controllerwire.CreateRunResponse) error {
			return handoffHereRun(ctx, options, response, defaultHereDependencies(flags.output))
		},
		follow: func(ctx context.Context, run string) (string, error) {
			followErr := followControllerRunLogs(ctx, options, run, flags.output, out)
			reader, err := client.NewRunReader(options)
			if err != nil {
				return "", errors.Join(followErr, err)
			}
			outcome, err := triggeredRunOutcome(ctx, reader, run, os.Stderr)
			return outcome, errors.Join(followErr, err)
		},
	})
}

// safety: the pin guards against sending a pipeline to the wrong controller by
// mistake; the controller still decides who may run it.
func refuseOtherThanPinnedProfile(pipelineName, profileName string, workingTree bool) error {
	// safety: the runner compiles the .sparkwing/ at the root of the checkout
	// the trigger submits, so that file holds the pin, never a nested one.
	top, err := gitOutput(context.Background(), ".", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("pipeline trigger: find the git checkout to read pipeline %s's pin: %w", pipelineName, err)
	}
	// safety: without --working-tree the controller runs the commit HEAD names,
	// so that commit's sparkwing.yaml holds the pin, not the working tree's.
	ref := "HEAD"
	if workingTree {
		ref = ""
	}
	entry, err := pinnedPipelineAt(filepath.Join(strings.TrimSpace(top), ".sparkwing"), ref, pipelineName)
	if err != nil {
		return fmt.Errorf("pipeline trigger: %w", err)
	}
	if entry == nil || entry.Profile == "" || entry.Profile == profileName {
		return nil
	}
	return fmt.Errorf("pipeline trigger: pipeline %s is pinned to profile %s; pass --profile %s", pipelineName, entry.Profile, entry.Profile)
}
