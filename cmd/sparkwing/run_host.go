package main

import (
	"context"
	"errors"
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

// safety: run options travel as host state, never as environment variables, so pipeline code cannot read or forge them.
func refuseUnhostedFlags(flags runFlags) error {
	switch {
	case flags.only != "":
		return errors.New("--sw-only is not supported by the engine host yet")
	case flags.workers > 0:
		return errors.New("--sw-workers is not supported by the engine host yet")
	}
	return nil
}

func (r *pipelineRun) host(h hostedRun) error {
	flags := h.flags
	program := r.uncachedBinary
	binarySource := "source"
	if program == "" {
		program, binarySource = r.lease.Path(), r.source
	}
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
	if flags.allowUnadmitted {
		if err := os.Setenv(orchestrator.AllowUnadmittedEnv, "1"); err != nil {
			return err
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
	if flags.verbose {
		extra["verbose"] = true
	}
	opts := orchestrator.Options{
		Pipeline:      h.schema.Name,
		RunHandlePath: h.runHandle,
		Args:          h.args,
		Git:           orchestrator.DetectGit(repoRoot),
		Debug:         orchestrator.DebugDirectivesFromEnv(),
		StartAt:       flags.startAt,
		StopAt:        flags.stopAt,
		NoCache:       flags.noCache,
		DryRun:        flags.dryRun,
		Priority:      h.priority,
	}
	// safety: the host takes over interrupts as the pipeline binary did, so
	// one Ctrl-C cancels the run once rather than killing this process.
	r.stopSignals()
	_, err := orchestrator.RunHostedCommand(context.WithoutCancel(r.ctx), opts, orchestrator.HostedEntry{
		Program: program, Dir: repoRoot, Env: h.env, SecretArgs: secretArgs,
		PipelineRevision: h.source, BinarySource: binarySource, Flags: extra,
	}, h.logFormat, flags.verbose)
	if err != nil {
		return exitError(1, err)
	}
	return nil
}
