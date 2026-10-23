package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type retryConnection struct {
	controllerwire.Controller
	here  func(context.Context, controllerwire.CreateRunResponse, string) error
	close func()
}

func runRunsRetry(ctx context.Context, inv clisurface.Invocation) error {
	return runRunsRetryCommand(ctx, inv, os.Stdin, os.Stdout, resolveRetryConnection)
}

func resolveRetryConnection(ctx context.Context, profile string) (retryConnection, error) {
	options, closeTransport, err := controllerOptions(ctx, profile)
	if err != nil {
		return retryConnection{}, err
	}
	controller, err := client.NewEngineClient(options)
	if err != nil {
		closeTransport()
		return retryConnection{}, err
	}
	return retryConnection{Controller: controller, close: closeTransport, here: func(ctx context.Context, response controllerwire.CreateRunResponse, output string) error {
		return handoffHereRun(ctx, options, response, defaultHereDependencies(output))
	}}, nil
}

func runRunsRetryCommand(ctx context.Context, inv clisurface.Invocation, input io.Reader, out io.Writer, resolve func(context.Context, string) (retryConnection, error)) error {
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
	connection, err := resolve(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer connection.close()
	mode := controllerwire.RetryFailed
	if inv.Bool("all-jobs") {
		mode = controllerwire.RetryAll
	}
	return retryRunIDs(ctx, connection, ids, mode, out, format)
}

type retryResult struct {
	SourceRunID string        `json:"source_run_id"`
	RunID       string        `json:"run_id,omitempty"`
	Number      int64         `json:"number,omitempty"`
	Replayed    bool          `json:"replayed"`
	Error       *apierr.Error `json:"error,omitempty"`
}

func retryRunIDs(ctx context.Context, connection retryConnection, ids []string, mode controllerwire.RetryMode, out io.Writer, format string) error {
	var failures []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		response, err := connection.Retry(ctx, id, controllerwire.RetryRequest{Mode: mode, IdempotencyKey: rand.Text()})
		result := retryResult{SourceRunID: id}
		if err == nil {
			result, err = retryReceipt(id, response)
		}
		if err == nil && response.Here {
			if connection.here == nil {
				err = incompleteHereHandoff(response.RunID, &apierr.Error{Code: apierr.Unavailable, Message: "here hosting is unavailable in this build"})
			} else {
				err = connection.here(ctx, response, format)
			}
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
	if response.RunID == "" || response.Number <= 0 || response.Inputs.Pipeline == "" {
		return result, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid retry receipt"}
	}
	result.RunID, result.Number, result.Replayed = response.RunID, response.Number, response.Replayed
	if response.HereBearer != "" && !response.Here {
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
