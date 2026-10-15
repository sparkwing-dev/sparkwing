package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func parseRunPrune(inv clisurface.Invocation) (time.Duration, error) {
	invalid := func(message string) (time.Duration, error) {
		return 0, &clisurface.UsageError{Path: inv.Command.Path, Message: message}
	}
	if inv.Has("run") == inv.Has("older-than") {
		return invalid("select either --run or --older-than")
	}
	if !inv.Has("older-than") {
		return 0, nil
	}
	d, err := clisurface.ParseLookback(inv.Value("older-than"))
	if err != nil || d <= 0 {
		return invalid("--older-than must be positive")
	}
	return d, nil
}

func runRunsPrune(ctx context.Context, args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "prune"}, args...))
	if err != nil {
		return err
	}
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	if _, err := parseRunPrune(inv); err != nil {
		return err
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	c, err := client.NewRunPruner(options)
	if err != nil {
		return err
	}
	return pruneObservedRuns(ctx, inv, c, os.Stdin, os.Stdout, time.Now())
}

type pruneRecord struct {
	RunID  string                      `json:"run_id"`
	DryRun bool                        `json:"dry_run,omitempty"`
	Result *controllerwire.RunDeletion `json:"result,omitempty"`
	Error  *apierr.Error               `json:"error,omitempty"`
}

func pruneObservedRuns(ctx context.Context, inv clisurface.Invocation, c controllerwire.RunPruner, input io.Reader, out io.Writer, now time.Time) error {
	age, err := parseRunPrune(inv)
	if err != nil {
		return err
	}
	ids, err := collectRunIDs(inv.Flags["run"], input)
	if err != nil {
		return err
	}
	if inv.Has("run") && len(ids) == 0 {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "at least one run is required"}
	}
	if age > 0 {
		cutoff := now.Add(-age)
		// safety: complete selection before deletes so removing rows cannot invalidate its cursors.
		err := visitRunPages(ctx, c, controllerwire.RunQuery{Limit: 100, StartedBefore: cutoff}, func(page controllerwire.RunPage) error {
			for _, run := range page.Runs {
				if isTerminalRunStatus(run.Status) && run.StartedAt != nil && run.StartedAt.Before(cutoff) {
					ids = append(ids, run.ID)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	format := inv.Output(color.IsInteractiveStdout())
	var failures []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		record := pruneRecord{RunID: id, DryRun: inv.Bool("dry-run")}
		if !record.DryRun {
			result, err := c.DeleteRun(ctx, id)
			if err != nil {
				failures = append(failures, fmt.Errorf("prune %s: %w", id, err))
				if !errors.As(err, &record.Error) {
					record.Error = &apierr.Error{Code: apierr.Internal, Message: err.Error()}
				}
			} else {
				record.Result = &result
			}
		}
		if err := writePruneRecord(out, record, format); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

func writePruneRecord(out io.Writer, record pruneRecord, format string) error {
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
	action := "deleted"
	if record.DryRun {
		action = "would prune"
	} else if !record.Result.Deleted {
		action = "already absent"
	}
	_, err := fmt.Fprintf(out, "%s: %s\n", record.RunID, action)
	return err
}
