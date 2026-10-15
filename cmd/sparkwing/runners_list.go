package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runRunnersList(ctx context.Context, args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"runners", "list"}, args...))
	if err != nil {
		return err
	}
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	reader, err := client.NewRunnerReader(options)
	if err != nil {
		return err
	}
	return listRunners(ctx, inv, reader, os.Stdout)
}

func listRunners(ctx context.Context, inv clisurface.Invocation, reader controllerwire.RunnerReader, out io.Writer) error {
	result, err := reader.ListRunners(ctx)
	if err != nil {
		return err
	}
	rows := result.Runners
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Class == rows[j].Class {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Class < rows[j].Class
	})
	format, quiet := inv.Output(color.IsInteractiveStdout()), inv.Bool("quiet")
	if format == "json" || format == "plain" || quiet {
		for _, row := range rows {
			if format == "json" {
				var record any = row
				if quiet {
					record = struct {
						Name string `json:"name"`
					}{row.Name}
				}
				if err := json.NewEncoder(out).Encode(record); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(out, row.Name); err != nil {
				return err
			}
		}
		return nil
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "(no runners)")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCLASS\tLOCATION\tSTATUS\tACTIVE\tHEADROOM\tLAST_SEEN\tLABELS\tRESERVED_POOLS\tMETERED")
	for _, row := range rows {
		headroom := "-"
		if row.Headroom != nil {
			headroom = fmt.Sprintf("%.1fc/%.1fGiB", row.Headroom.Cores, float64(row.Headroom.MemoryBytes)/float64(1<<30))
		}
		seen := "-"
		if row.LastSeen != "" {
			observed, err := time.Parse(time.RFC3339Nano, row.LastSeen)
			if err != nil {
				return fmt.Errorf("invalid runner last_seen: %w", err)
			}
			seen = observed.UTC().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\t%s\t%s\t%t\n", row.Name, row.Class, row.Location, row.Status, len(row.ActiveJobs), row.MaxConcurrent, headroom, seen, joinOrDash(row.Labels), joinOrDash(row.ReservedPools), row.Metered)
	}
	return tw.Flush()
}
