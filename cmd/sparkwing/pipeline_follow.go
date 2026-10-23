package main

import (
	"context"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

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
	if err := writeEngineNotices(out, detail.Notices); err != nil {
		return "", err
	}
	if detail.Status != "success" {
		if _, err := fmt.Fprintf(out, "\nrun %s\nstatus:    %s\n", detail.ID, detail.Status); err != nil {
			return "", err
		}
		if err := writeRunErrors(out, detail, "pretty"); err != nil {
			return "", err
		}
	}
	return detail.Status, nil
}
