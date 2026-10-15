package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type hostedRun struct {
	schema    sparkwing.DescribePipeline
	args      map[string]string
	flags     runFlags
	env       []string
	source    string
	priority  string
	logFormat string
	runHandle string
}

func (r *pipelineRun) host(h hostedRun) error {
	flags := h.flags
	execDir := r.opts.ExecutionDir
	if execDir == "" {
		execDir = r.sparkwingDir
	}
	repoRoot := filepath.Dir(execDir)
	// safety: the daemon this run may start must be this binary, which is
	// what the pipeline binary was pointed at before the host moved here.
	if os.Getenv(wingdclient.HostBinEnv) == "" {
		if self, err := os.Executable(); err == nil {
			if err := os.Setenv(wingdclient.HostBinEnv, self); err != nil {
				return err
			}
		}
	}
	var secretArgs []string
	for _, a := range h.schema.Args {
		if a.Secret {
			secretArgs = append(secretArgs, a.Name)
		}
	}
	extra := map[string]any{}
	if len(flags.allow) > 0 {
		extra["allow"] = strings.Join(flags.allow, ",")
	}
	if flags.ref != "" {
		extra["ref"] = flags.ref
	}
	if flags.noUpdate {
		extra["no_update"] = true
	}
	if flags.mode != "" {
		extra["mode"] = flags.mode
	}
	if flags.only != "" {
		extra["only"] = flags.only
	}
	if flags.workers > 0 {
		extra["workers"] = flags.workers
	}
	debug := orchestrator.DebugDirectivesFromEnv()
	if flags.pauseBefore != nil {
		debug.PauseBefore = flags.pauseBefore
	}
	if flags.pauseAfter != nil {
		debug.PauseAfter = flags.pauseAfter
	}
	if flags.pauseOnFailure != nil {
		debug.PauseOnFailure = *flags.pauseOnFailure
	}
	opts := orchestrator.Options{
		Pipeline:      h.schema.Name,
		RunHandlePath: h.runHandle,
		Args:          h.args,
		Git:           orchestrator.DetectGit(repoRoot),
		Debug:         debug,
		StartAt:       flags.startAt,
		StopAt:        flags.stopAt,
		Only:          flags.only,
		MaxParallel:   flags.workers,
		NoCache:       flags.noCache,
		DryRun:        flags.dryRun,
		Priority:      h.priority,

		AllowUnadmitted: flags.allowUnadmitted,
		Verbose:         flags.verbose,
	}
	// safety: the host takes over interrupts as the pipeline binary did, so
	// one Ctrl-C cancels the run once rather than killing this process.
	r.stopSignals()
	_, err := orchestrator.RunHostedCommand(context.WithoutCancel(r.ctx), opts, orchestrator.HostedEntry{
		Command: r.entry, Dir: repoRoot, Env: h.env, SecretArgs: secretArgs,
		PipelineRevision: h.source, BinarySource: r.source, Flags: extra,
	}, h.logFormat)
	if err != nil {
		return exitError(1, err)
	}
	return nil
}
