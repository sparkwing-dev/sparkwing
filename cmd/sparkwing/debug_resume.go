package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type debugResumer struct {
	reader controllerwire.DebugReader
	resume func(context.Context, string, string) error
	close  func() error
}

func resolveDebugResumer(context.Context, string) (debugResumer, error) {
	return debugResumer{}, &apierr.Error{Code: apierr.PolicyMissing, Message: "debug resume requires a registered controller pause-release policy"}
}

func runDebugResume(ctx context.Context, args []string, out io.Writer, resolve func(context.Context, string) (debugResumer, error)) (resultErr error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"debug", "resume"}, args...))
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, err := resolve(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	if runtime.close != nil {
		defer func() { resultErr = errors.Join(resultErr, runtime.close()) }()
	}
	if runtime.reader == nil || runtime.resume == nil || runtime.close == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "debug resume requires controller observation, release and cleanup bindings"}
	}
	job, err := runtime.reader.ReadDebugJob(ctx, inv.Value("run"), inv.Value("job"))
	if err != nil {
		return err
	}
	if err := job.Validate(inv.Value("run"), inv.Value("job")); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runtime.resume(ctx, job.RunID, job.Job.ID); err != nil {
		return err
	}
	result := struct {
		RunID   string `json:"run_id"`
		JobID   string `json:"job_id"`
		Resumed bool   `json:"resumed"`
	}{job.RunID, job.Job.ID, true}
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	if format == "plain" {
		_, err = fmt.Fprintf(out, "%s/%s\n", result.RunID, result.JobID)
	} else {
		_, err = fmt.Fprintf(out, "resumed %s/%s\n", result.RunID, result.JobID)
	}
	return err
}
