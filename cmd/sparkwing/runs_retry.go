package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runRunsRetry(ctx context.Context, args []string) error {
	return runRunsRetryCommand(ctx, args, os.Stdin, os.Stdout, resolveEngineController)
}

func runRunsRetryCommand(ctx context.Context, args []string, input io.Reader, out io.Writer, resolve func(context.Context, string) (controllerwire.Controller, func(), error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "retry"}, args...))
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	if inv.Bool("failed") == inv.Bool("all-jobs") {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "select --failed or --all-jobs"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ids, err := collectRunIDs(inv.Flags["run"], input)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "at least one run is required"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	controller, closeTransport, err := resolve(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	mode := controllerwire.RetryFailed
	if inv.Bool("all-jobs") {
		mode = controllerwire.RetryAll
	}
	return retryRunIDs(ctx, controller, ids, mode, out, format)
}

type retryResult struct {
	SourceRunID string        `json:"source_run_id"`
	RunID       string        `json:"run_id,omitempty"`
	Number      int64         `json:"number,omitempty"`
	Replayed    bool          `json:"replayed"`
	Error       *apierr.Error `json:"error,omitempty"`
}

func retryRunIDs(ctx context.Context, controller controllerwire.Controller, ids []string, mode controllerwire.RetryMode, out io.Writer, format string) error {
	var failures []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		response, err := controller.Retry(ctx, id, controllerwire.RetryRequest{Mode: mode, IdempotencyKey: rand.Text()})
		result := retryResult{SourceRunID: id}
		if err == nil {
			result, err = retryReceipt(id, response)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("retry run %s: %w", id, err))
			if !errors.As(err, &result.Error) {
				result.Error = &apierr.Error{Code: apierr.Internal, Message: err.Error()}
			}
		}
		if err := writeRetryResult(out, result, format); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(append(failures, ctx.Err())...)
}

func retryReceipt(source string, response controllerwire.CreateRunResponse) (retryResult, error) {
	result := retryResult{SourceRunID: source}
	if response.RunID == "" || response.Number <= 0 || response.Request.RunID == "" {
		return result, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid retry receipt"}
	}
	result.RunID, result.Number, result.Replayed = response.RunID, response.Number, response.Replayed
	// safety: private intake state and one-time authority never enter the public receipt.
	if response.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere {
		return result, exitError(3, &apierr.Error{Code: apierr.Unavailable, Message: "retry created run " + response.RunID + " but here hosting is unavailable in this build", Hint: "the here handoff is incomplete; the reservation retains its original expiry"})
	}
	if response.HereBearer != "" {
		return result, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned unexpected here authority"}
	}
	return result, nil
}

func writeRetryResult(out io.Writer, result retryResult, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	if result.Error != nil {
		return nil
	}
	if format == "plain" {
		_, err := fmt.Fprintln(out, result.RunID)
		return err
	}
	message := "submitted"
	if result.Replayed {
		message = "already submitted"
	}
	_, err := fmt.Fprintf(out, "run %s %s (retry of %s)\n", result.RunID, message, result.SourceRunID)
	return err
}
