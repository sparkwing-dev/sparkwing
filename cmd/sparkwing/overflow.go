package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runOverflow(ctx context.Context, inv clisurface.Invocation) error {
	return runOverflowCommand(ctx, inv, os.Stdout, func(ctx context.Context, profile string) (controllerwire.OverflowManager, func(), error) {
		options, closeTransport, err := controllerOptions(ctx, profile)
		if err != nil {
			return nil, nil, err
		}
		manager, err := client.NewOverflowManager(options)
		if err != nil {
			closeTransport()
			return nil, nil, err
		}
		return manager, closeTransport, nil
	})
}

func runOverflowCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect func(context.Context, string) (controllerwire.OverflowManager, func(), error)) error {
	if inv.Help() || inv.Command.Path == "sparkwing overflow" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, inv.Output(color.IsInteractiveStdout()))
	}
	manager, closeTransport, err := connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	return manageOverflow(ctx, inv, manager, out)
}

func manageOverflow(ctx context.Context, inv clisurface.Invocation, manager controllerwire.OverflowManager, out io.Writer) error {
	var result controllerwire.OverflowControl
	var err error
	switch inv.Command.Path {
	case "sparkwing overflow show":
		result, err = manager.ReadOverflowControl(ctx)
	case "sparkwing overflow set":
		if inv.Has("minutes") == inv.Bool("uncapped") {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "exactly one of --minutes or --uncapped is required"}
		}
		var minutes *int64
		if inv.Has("minutes") {
			value, parseErr := strconv.ParseInt(inv.Value("minutes"), 10, 64)
			if parseErr != nil || value < 0 || value > math.MaxInt64/60 {
				return &clisurface.UsageError{Path: inv.Command.Path, Message: "--minutes requires a non-negative representable number of minutes"}
			}
			minutes = &value
		}
		result, err = manager.SetOverflowLimit(ctx, minutes)
	default:
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "unknown overflow command"}
	}
	if err != nil {
		return err
	}
	if inv.Output(color.IsInteractiveStdout()) == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	limit := "uncapped"
	if result.MonthlyMinutes != nil {
		limit = strconv.FormatInt(*result.MonthlyMinutes, 10)
	}
	if inv.Output(color.IsInteractiveStdout()) == "plain" {
		_, err = fmt.Fprintln(out, limit)
		return err
	}
	if result.MonthlyMinutes != nil {
		limit += " minutes"
	}
	_, err = fmt.Fprintf(out, "Monthly limit: %s\nUsed: %d seconds\nPeriod start: %s\n", limit, result.UsedSeconds, result.PeriodStart.UTC().Format(time.RFC3339))
	return err
}
