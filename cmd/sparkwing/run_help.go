package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func runSelectedHelp(dir, pipeline string, flags runFlags, selectEngine func(string, func()) error) error {
	captured := os.Environ()
	var selections []func()
	closeSelections := func() {
		for i := len(selections) - 1; i >= 0; i-- {
			selections[i]()
		}
	}
	defer closeSelections()
	executionDir := dir
	if flags.ref != "" {
		_, selected, cleanup, err := setupRefWorktree(dir, flags.ref)
		if err != nil {
			return err
		}
		selections = append(selections, cleanup)
		executionDir = selected
	}
	config, err := projectconfig.Load(filepath.Join(executionDir, projectconfig.Filename))
	if err != nil {
		return err
	}
	identity, err := clisurface.ResolvePipeline(config, pipeline)
	if err != nil {
		return err
	}
	environment, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: captured, Masker: secrets.NewMasker()}, procenv.Runtime{})
	if err != nil {
		return err
	}
	checkout := triggerCheckout{directory: filepath.Dir(dir), environment: environment}
	revision, err := triggerPipelineRevision(context.Background(), checkout, config, pipeline, flags.pipelineRef, "")
	if err != nil {
		return err
	}
	sourceDir := executionDir
	if revision != "" {
		_, selected, cleanup, err := setupRefWorktree(dir, revision)
		if err != nil {
			return err
		}
		selections = append(selections, cleanup)
		sourceDir = selected
	}
	if err := selectEngine(sourceDir, closeSelections); err != nil {
		return err
	}
	run := newPipelineRun(sourceDir, compileOptions{NoUpdate: flags.noUpdate, NoBincache: flags.noBincache, HashAllFiles: flags.hashAllFiles, ExecutionDir: executionDir})
	defer run.stop()
	run.ctx = gotoolchain.WithSession(run.ctx, captured, nil)
	if err := run.materialize(captured); err != nil {
		return run.finish(err)
	}
	described, err := describeEntry(run.ctx, executionDir, run.entry)
	if err != nil {
		return run.finish(err)
	}
	schema, err := boundPipelineSchema(described.Pipelines, identity)
	if err == nil {
		var text bytes.Buffer
		printPipelineHelp(&text, schema)
		err = writeHelpText(cmdRun, os.Stdout, flags.outputFormat, text.String())
	}
	return run.finish(err)
}
