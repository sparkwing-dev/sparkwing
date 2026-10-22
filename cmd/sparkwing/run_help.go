package main

import (
	"bytes"
	"context"
	"os"
)

func runSelectedHelp(dir, pipeline string, flags runFlags) error {
	return withPipelineEntryAt(dir, pipeline, flags, func(ctx context.Context, entry pipelineInspection, _ string) error {
		schema, err := entry.presentation(ctx)
		if err != nil {
			return err
		}
		var text bytes.Buffer
		printPipelineHelp(&text, schema)
		return writeHelpText(cmdRun, os.Stdout, flags.outputFormat, text.String())
	})
}
