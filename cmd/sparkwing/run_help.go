package main

import (
	"bytes"
	"context"
	"errors"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func runSelectedHelp(dir, pipeline string, flags runFlags, selectEngine func(string, func()) error) (resultErr error) {
	captured := os.Environ()
	home, err := wingd.HomeDir("")
	if err != nil {
		return err
	}
	selected, err := selectLocalRunSource(context.Background(), home, dir, pipeline, flags, captured, secrets.NewMasker())
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, selected.execution.Close()) }()
	entry, err := selected.checkout(context.Background(), home, captured)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, entry.Close()) }()
	executionDir, sourceDir, identity := selected.execution.Directory, entry.Directory, selected.identity
	var closeErr error
	closeSelections := func() { closeErr = errors.Join(closeErr, entry.Close(), selected.execution.Close()) }
	if err := selectEngine(sourceDir, closeSelections); err != nil {
		return errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return closeErr
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
