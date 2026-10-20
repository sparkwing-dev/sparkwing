package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type debugResumer struct {
	control controllerwire.DebugControl
	close   func() error
}

func resolveDebugResumer(ctx context.Context, profile string) (debugResumer, error) {
	options, closeTransport, err := controllerOptions(ctx, profile)
	if err != nil {
		return debugResumer{}, err
	}
	control, err := client.NewDebugControl(options)
	if err != nil {
		closeTransport()
		return debugResumer{}, err
	}
	return debugResumer{control: control, close: func() error {
		closeTransport()
		return nil
	}}, nil
}

func runDebugResume(ctx context.Context, inv clisurface.Invocation, out io.Writer, resolve func(context.Context, string) (debugResumer, error)) (resultErr error) {
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
	if runtime.control == nil || runtime.close == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "debug resume requires controller observation, release and cleanup bindings"}
	}
	pause, err := runtime.control.ReadDebugPause(ctx, inv.Value("run"), inv.Value("job"))
	if err != nil {
		return err
	}
	if err := pause.Validate(inv.Value("run"), inv.Value("job")); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runtime.control.ResumeDebugPause(ctx, pause.RunID, pause.JobID); err != nil {
		return err
	}
	result := struct {
		RunID   string `json:"run_id"`
		JobID   string `json:"job_id"`
		Resumed bool   `json:"resumed"`
	}{pause.RunID, pause.JobID, true}
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
