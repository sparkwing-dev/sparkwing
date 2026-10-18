package main

import (
	"context"
	"errors"
	"io"
	"os"

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

func triggerFlagsFromInvocation(inv clisurface.Invocation) (triggerFlags, error) {
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

func runPipelineTrigger(inv clisurface.Invocation) error {
	ctx, stop, _ := interruptContext()
	defer stop()
	return runTriggerCommand(ctx, inv, os.Stdout, prepareNativeTrigger, controllerOptions)
}

func runTriggerCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, prepare func(context.Context, triggerFlags) (controllerwire.CreateRunRequest, error), connect func(context.Context, string) (client.EngineOptions, func(), error)) error {
	flags, err := triggerFlagsFromInvocation(inv)
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
