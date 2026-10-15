package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runRunsCancel(ctx context.Context, args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "cancel"}, args...))
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, format)
	}
	ids, err := collectRunIDs(inv.Flags["run"], os.Stdin)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "at least one run is required"}
	}
	c, closeTransport, err := resolveEngineController(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	return cancelRunIDs(ctx, c, ids, os.Stdout, format)
}

type cancellationResult struct {
	RunID   string                  `json:"run_id"`
	Applied *controllerwire.Applied `json:"applied,omitempty"`
	Error   *apierr.Error           `json:"error,omitempty"`
}

func cancelRunIDs(ctx context.Context, controller controllerwire.Controller, ids []string, out io.Writer, format string) error {
	var failures []error
	for _, id := range ids {
		applied, err := controller.Cancel(ctx, id, controllerwire.CancelRequest{})
		record := cancellationResult{RunID: id}
		if err == nil {
			record.Applied = &applied
		} else {
			failures = append(failures, fmt.Errorf("run %s: %w", id, err))
			if !errors.As(err, &record.Error) {
				record.Error = &apierr.Error{Code: apierr.Internal, Message: err.Error()}
			}
		}
		if err := writeCancellation(out, record, format); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
	}
	return errors.Join(failures...)
}

func writeCancellation(out io.Writer, record cancellationResult, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(record)
	}
	if record.Error != nil {
		return nil
	}
	if format == "plain" {
		_, err := fmt.Fprintln(out, record.RunID)
		return err
	}
	message := "cancellation requested"
	if record.Applied.Replayed {
		message = "cancellation already applied"
	}
	_, err := fmt.Fprintf(out, "%s: %s\n", record.RunID, message)
	return err
}
