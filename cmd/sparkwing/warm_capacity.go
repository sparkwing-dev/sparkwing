package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runWarmCapacityCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, connect operatorConnector) error {
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing operator warm-capacity" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	input := controllerwire.WarmCapacitySettings{Class: inv.Value("class"), Mode: inv.Value("mode"), StartTimeTargetSeconds: 20}
	setting := inv.Command.Path == "sparkwing operator warm-capacity set"
	if setting {
		for _, field := range []struct {
			name  string
			value *int64
		}{{"count", &input.ManualCount}, {"min", &input.AutoMin}, {"max", &input.AutoMax}} {
			if raw := inv.Value(field.name); raw != "" {
				n, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || n < 0 {
					return &clisurface.UsageError{Path: inv.Command.Path, Message: "--" + field.name + " must be a nonnegative integer"}
				}
				*field.value = n
			}
		}
		if raw := inv.Value("start-time-target"); raw != "" {
			target, err := time.ParseDuration(raw)
			if err != nil || target <= 0 {
				return &clisurface.UsageError{Path: inv.Command.Path, Message: "--start-time-target must be a positive duration, such as 20s"}
			}
			input.StartTimeTargetSeconds = target.Seconds()
		}
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
	var view controllerwire.WarmCapacity
	if setting {
		view, err = manager.WriteWarmCapacity(ctx, input)
	} else {
		view, err = manager.ReadWarmCapacity(ctx)
	}
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(view)
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLASS\tMODE\tMANUAL\tMIN\tMAX\tTARGET\tWARM\tDESIRED\tp50/p95 1h\tp50/p95 24h\tCOLD 24h/7d\tUPSIZES 24h/7d")
	for _, c := range view.Classes {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%gs\t%d\t%d\t%s/%s\t%s/%s\t%d/%d\t%d/%d\n", c.Class, c.Mode, c.ManualCount, c.AutoMin, c.AutoMax, c.StartTimeTargetSeconds, c.Warm, c.Desired, warmSeconds(c.Start1h.P50Seconds), warmSeconds(c.Start1h.P95Seconds), warmSeconds(c.Start24h.P50Seconds), warmSeconds(c.Start24h.P95Seconds), c.ColdStarts24h, c.ColdStarts7d, c.Upsizes24h, c.Upsizes7d)
	}
	fmt.Fprintf(tw, "New signups (24h/7d)\t%d/%d\nActive teams (24h/7d)\t%d/%d\n", view.NewSignups24h, view.NewSignups7d, view.ActiveTeams24h, view.ActiveTeams7d)
	return tw.Flush()
}

func warmSeconds(value *float64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatFloat(*value, 'f', 2, 64) + "s"
}
