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

	"github.com/sparkwing-dev/sparkwing/internal/opsview"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runRunnersList(ctx context.Context, inv clisurface.Invocation) error {
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
	return listRunners(ctx, inv, reader, os.Stdout, locallyPausedRunner(options.URL))
}

// safety: the pause is a local file, so only this machine's enrolled runner, enrolled with the
// listed controller, can be shown paused.
func locallyPausedRunner(controllerURL string) string {
	path, err := agentConfigPath("")
	if err != nil {
		return ""
	}
	cfg, err := userconfig.ReadConfig(path)
	if err != nil || cfg.Runner == nil || cfg.Runner.Controller == nil || cfg.Runner.HolderPrefix == nil || cfg.Runner.CredentialsDir == nil {
		return ""
	}
	configured, err := normalizeControllerURL(*cfg.Runner.Controller)
	if err != nil {
		return ""
	}
	if listed, err := normalizeControllerURL(controllerURL); err != nil || listed != configured {
		return ""
	}
	if paused, err := runners.Paused(*cfg.Runner.CredentialsDir); err != nil || !paused {
		return ""
	}
	return *cfg.Runner.HolderPrefix
}

func listRunners(ctx context.Context, inv clisurface.Invocation, reader controllerwire.RunnerReader, out io.Writer, pausedName string) error {
	result, err := reader.ListRunners(ctx)
	if err != nil {
		return err
	}
	rows := result.Runners
	if pausedName != "" {
		for i := range rows {
			if rows[i].Name == pausedName {
				rows[i].Status = "paused"
			}
		}
	}
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
			} else if _, err := fmt.Fprintln(out, opsview.DisplayText(row.Name)); err != nil {
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\t%s\t%s\t%t\n", opsview.DisplayText(row.Name), opsview.DisplayText(row.Class), opsview.DisplayText(row.Location), opsview.DisplayText(row.Status), len(row.ActiveJobs), row.MaxConcurrent, headroom, seen, opsview.DisplayText(joinOrDash(row.Labels)), opsview.DisplayText(joinOrDash(row.ReservedPools)), row.Metered)
	}
	return tw.Flush()
}
