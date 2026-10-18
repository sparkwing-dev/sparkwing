package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type operatorConnector func(context.Context, string) (controllerwire.OperatorManager, func(), error)

func resolveOperatorManager(ctx context.Context, profile string) (controllerwire.OperatorManager, func(), error) {
	options, closeTransport, err := controllerOptions(ctx, profile)
	if err != nil {
		return nil, nil, err
	}
	manager, err := client.NewOperatorManager(options)
	if err != nil {
		closeTransport()
		return nil, nil, err
	}
	return manager, closeTransport, nil
}

func runOperatorLimits(inv clisurface.Invocation) error {
	return runOperatorLimitsCommand(context.Background(), inv, os.Stdout, resolveOperatorManager)
}

func budgetRows(view controllerwire.OperatorLimits) [][2]string {
	idle := computeLimitLabel(view.Budgets.IdleClaimPollSeconds)
	if view.Budgets.IdleClaimPollSeconds > 0 {
		idle = strconv.FormatInt(view.Budgets.IdleClaimPollSeconds, 10) + "s"
		if view.Budgets.IdleClaimPollEnforced {
			idle += ", enforced"
		}
	}
	return [][2]string{
		{"claims_per_runner_minute", computeLimitLabel(view.Budgets.ClaimsPerRunnerMinute)},
		{"heartbeats_per_runner_minute", computeLimitLabel(view.Budgets.HeartbeatsPerRunnerMinute)},
		{"idle_claim_poll", idle},
		{"max_log_streams_per_principal", computeLimitLabel(view.Budgets.MaxLogStreamsPerPrincipal)},
		{"max_downloads_per_principal", computeLimitLabel(view.Budgets.MaxDownloadsPerPrincipal)},
		{"requests_per_token_minute", computeLimitLabel(view.Budgets.RequestsPerTokenMinute)},
		{"requests_per_minute_alarm", computeLimitLabel(view.Budgets.RequestsPerMinuteAlarm)},
	}
}

func runOperatorLimitsCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect operatorConnector) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing operator limits" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	var input controllerwire.OperatorLimitsWrite
	if inv.Command.Path == "sparkwing operator limits set" {
		value, err := strconv.ParseInt(inv.Value("value"), 10, 64)
		if err != nil {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: "--value must be a nonnegative integer"}
		}
		input.Limits = map[string]int64{inv.Value("setting"): value}
		if err := input.Validate(); err != nil {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, closeTransport, err := connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	if err := ctx.Err(); err != nil {
		return err
	}
	var view controllerwire.OperatorLimits
	if input.Limits != nil {
		view, err = manager.WriteOperatorLimits(ctx, input)
	} else {
		view, err = manager.ReadOperatorLimits(ctx)
	}
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(view)
	}
	var buffer bytes.Buffer
	if format == "plain" {
		err = writeComputeLimitsPlain(&buffer, view)
	} else {
		err = renderComputeLimits(&buffer, view)
	}
	_, writeErr := out.Write(buffer.Bytes())
	return errors.Join(err, writeErr)
}

func renderComputeLimits(w io.Writer, view controllerwire.OperatorLimits) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, name := range sortedPrincipals(view.Limits) {
		fmt.Fprintf(tw, "%s\t%s\n", strings.ToUpper(name), computeLimitLabel(view.Limits[name]))
	}
	if view.Usage.Runners != nil {
		fmt.Fprintf(tw, "CLOUD RUNNERS\t%d claimed now\n", *view.Usage.Runners)
	}
	for _, row := range budgetRows(view) {
		fmt.Fprintf(tw, "%s\t%s\n", strings.ToUpper(row[0]), row[1])
	}
	if view.Usage.AlarmReached != nil && *view.Usage.AlarmReached {
		fmt.Fprintf(tw, "ALARM\treached\n")
	}
	for _, principal := range sortedPrincipals(view.Usage.ByPrincipal) {
		fmt.Fprintf(tw, "  %s\t%d\n", principal, view.Usage.ByPrincipal[principal])
	}
	return tw.Flush()
}

func writeComputeLimitsPlain(w io.Writer, view controllerwire.OperatorLimits) error {
	for _, name := range sortedPrincipals(view.Limits) {
		if _, err := fmt.Fprintf(w, "%s\t%d\n", name, view.Limits[name]); err != nil {
			return err
		}
	}
	for _, row := range budgetRows(view) {
		if _, err := fmt.Fprintf(w, "%s\t%s\n", row[0], row[1]); err != nil {
			return err
		}
	}
	if view.Usage.Runners != nil {
		if _, err := fmt.Fprintf(w, "runners\t%d\n", *view.Usage.Runners); err != nil {
			return err
		}
	}
	for _, principal := range sortedPrincipals(view.Usage.ByPrincipal) {
		if _, err := fmt.Fprintf(w, "runners.%s\t%d\n", principal, view.Usage.ByPrincipal[principal]); err != nil {
			return err
		}
	}
	return nil
}

func sortedPrincipals(held map[string]int64) []string {
	out := make([]string, 0, len(held))
	for principal := range held {
		out = append(out, principal)
	}
	sort.Strings(out)
	return out
}

func computeLimitLabel(value int64) string {
	if value <= 0 {
		return "unlimited"
	}
	return strconv.FormatInt(value, 10)
}
