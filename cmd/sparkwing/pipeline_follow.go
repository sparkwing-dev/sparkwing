package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func configurationRefusal(detail controllerwire.RunDetail) *apierr.Error {
	if detail.Status == "failed" {
		for _, notice := range detail.Notices {
			if notice.Kind == engineid.NoticeSecretPreflightRefused && notice.Reason != "" {
				return &apierr.Error{Code: apierr.BadRequest, Message: notice.Reason, Hint: notice.Hint}
			}
		}
	}
	return nil
}

func runCompletionError(ctx context.Context, reader interface {
	ReadRun(context.Context, string) (controllerwire.RunDetail, error)
}, run, status string,
) error {
	if status == "failed" {
		if detail, err := reader.ReadRun(ctx, run); err == nil && detail.ID == run {
			if refusal := configurationRefusal(detail); refusal != nil {
				return exitError(1, refusal)
			}
		}
	}
	return statusExitCode(status)
}

// foregroundRunCompletion follows a failed run's last line with its failure block in
// pretty and plain output; the block states the outcome, so no diagnostic repeats it.
func foregroundRunCompletion(ctx context.Context, reader interface {
	ReadRun(context.Context, string) (controllerwire.RunDetail, error)
}, run, status, format string, out io.Writer,
) error {
	failure := statusExitCode(status)
	if failure == nil || format == "json" {
		return runCompletionError(ctx, reader, run, status)
	}
	detail, err := reader.ReadRun(ctx, run)
	if err != nil || detail.ID != run {
		return failure
	}
	if refusal := configurationRefusal(detail); refusal != nil {
		return exitError(1, refusal)
	}
	if err := writeFailureBlock(out, withSteps(ctx, reader, detail), "", ""); err != nil {
		return errors.Join(failure, err)
	}
	return &cliError{code: 1, err: failure, reported: true}
}

func triggeredRunOutcome(ctx context.Context, reader controllerwire.RunReader, runID string, out io.Writer) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	detail, err := reader.ReadRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if detail.ID != runID || detail.Status == "" {
		return "", &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid run outcome"}
	}
	if out == nil {
		out = io.Discard
	}
	if err := writeEngineNoticesOnce(out, detail.Notices); err != nil {
		return "", err
	}
	if detail.Status != "success" {
		if _, err := fmt.Fprintf(out, "\nrun %s\nstatus:    %s\n", opsview.DisplayText(detail.ID), opsview.DisplayText(detail.Status)); err != nil {
			return "", err
		}
		if err := writeRunErrors(out, detail, "pretty"); err != nil {
			return "", err
		}
	}
	if refusal := configurationRefusal(detail); refusal != nil {
		return detail.Status, exitError(1, refusal)
	}
	return detail.Status, nil
}
