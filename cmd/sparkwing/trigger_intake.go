package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type triggerSubmission struct {
	controllerwire.Controller
	here   func(context.Context, controllerwire.CreateRunResponse) error
	follow func(context.Context, string) (string, error)
}

type triggerReceipt struct {
	RunID    string        `json:"run_id"`
	Number   int64         `json:"number"`
	Replayed bool          `json:"replayed"`
	Outcome  string        `json:"outcome,omitempty"`
	Error    *apierr.Error `json:"error,omitempty"`
}

func submitTriggeredRun(ctx context.Context, input controllerwire.CreateRunRequest, detached bool, format string, out io.Writer, connection triggerSubmission) (resultErr error) {
	masker := secrets.NewMasker()
	for _, value := range input.LocalSecrets {
		masker.Register(value)
	}
	for _, value := range input.SubmittedEnv {
		masker.Register(string(value))
	}
	for _, value := range input.Request.RequestedArgs {
		masker.Register(value)
	}
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	here := input.Request.Options.Runner.Set && input.Request.Options.Runner.Value.Kind == runspec.RunnerHere
	if detached && here {
		return &clisurface.UsageError{Path: "sparkwing pipeline trigger", Message: "--runner here cannot --detach"}
	}
	if format != "json" && format != "plain" && format != "pretty" {
		return &clisurface.UsageError{Path: "sparkwing pipeline trigger", Message: "output must be json, plain or pretty"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if connection.Controller == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "trigger intake requires a controller"}
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = rand.Text()
	}
	response, err := connection.CreateRun(ctx, input)
	masker.Register(response.HereBearer)
	for _, value := range response.Request.Request.RequestedArgs {
		masker.Register(value)
	}
	if err != nil {
		return err
	}
	receipt, err := initialTriggerReceipt(input.Request.Pipeline, here, response)
	if err != nil {
		return err
	}
	if here {
		if connection.here == nil {
			resultErr = incompleteHereHandoff(response.RunID, &apierr.Error{Code: apierr.Unavailable, Message: "here hosting requires a shared host binding"})
		} else {
			resultErr = connection.here(ctx, response)
		}
	} else if err := ctx.Err(); err != nil {
		resultErr = exitError(3, err)
	} else if !detached {
		receipt.Outcome, resultErr = followTriggeredRun(ctx, response.RunID, connection.follow)
	}
	if resultErr != nil {
		masked := &maskedCLIError{err: resultErr, masker: masker}
		if !errors.As(masked, &receipt.Error) {
			receipt.Error = &apierr.Error{Code: apierr.Internal, Message: masked.Error()}
		}
	}
	return errors.Join(resultErr, writeTriggerReceipt(out, receipt, format))
}

func initialTriggerReceipt(pipeline string, here bool, response controllerwire.CreateRunResponse) (triggerReceipt, error) {
	receipt := triggerReceipt{RunID: response.RunID, Number: response.Number, Replayed: response.Replayed}
	seal := response.Request
	hash, err := runspec.CanonicalHash(seal)
	sealedHere := seal.Request.Options.Runner.Set && seal.Request.Options.Runner.Value.Kind == runspec.RunnerHere
	validIdentity := response.RunID != "" && response.Number > 0 && seal.RunID == response.RunID && seal.Request.Pipeline == pipeline
	validSeal := err == nil && seal.Version == 1 && seal.Request.Version == 1 && seal.Hash != "" && hash == seal.Hash
	expectsBearer := here && !response.Replayed
	if !validIdentity || !validSeal || sealedHere != here || (response.HereBearer != "") != expectsBearer {
		return triggerReceipt{}, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid initial intake receipt"}
	}
	return receipt, nil
}

func followTriggeredRun(ctx context.Context, run string, follow func(context.Context, string) (string, error)) (string, error) {
	if follow == nil {
		return "", exitError(3, &apierr.Error{Code: apierr.Unavailable, Message: "trigger observation requires a run follower"})
	}
	outcome, err := follow(ctx, run)
	if err := errors.Join(err, ctx.Err()); err != nil {
		return "", exitError(3, err)
	}
	if !isTerminalRunStatus(outcome) {
		return "", exitError(3, &apierr.Error{Code: apierr.Unavailable, Message: "triggered run outcome is unknown"})
	}
	return outcome, statusExitCode(outcome)
}

func writeTriggerReceipt(out io.Writer, receipt triggerReceipt, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(receipt)
	}
	if format == "plain" {
		_, err := fmt.Fprintln(out, receipt.RunID)
		return err
	}
	_, err := fmt.Fprintf(out, "run %d (%s)\n", receipt.Number, receipt.RunID)
	return err
}
