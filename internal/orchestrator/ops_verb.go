package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func RunOps(args []string) error { return runOpsCLI(args) }

func runOpsCLI(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ops queue|doctor|stats|stats-reset|version [flags]")
	}
	rest := args[1:]
	switch args[0] {
	case "version":
		return runOpsVersion(rest)
	case "queue":
		return runOpsQueue(rest)
	case "stats":
		return runOpsStats(rest)
	case "stats-reset":
		return runOpsStatsReset(rest)
	case "doctor":
		return runOpsDoctor(rest)
	default:
		return fmt.Errorf("ops: unknown verb %q (want queue|doctor|stats|stats-reset|version)", args[0])
	}
}

func opsOutputFlags(fs *flag.FlagSet) func() string {
	o := fs.String("o", "", "output format: pretty|json|plain")
	output := fs.String("output", "", "output format: pretty|json|plain")
	return func() string {
		if *o != "" {
			return *o
		}
		return *output
	}
}

func resolveOpsFormat(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if isInteractiveStdout() {
		return "pretty"
	}
	return "json"
}

func runOpsQueue(args []string) error {
	fs := flag.NewFlagSet("ops queue", flag.ContinueOnError)
	getOut := opsOutputFlags(fs)
	home := fs.String("home", "", "sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	format := resolveOpsFormat(getOut())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: *home, Version: sparkwingModuleVersion()})
	if err != nil {
		if errors.Is(err, wingdclient.ErrDaemonUnreachable) {
			if rerr := opsview.RenderUnreachableDaemon(os.Stdout, format, err); rerr != nil {
				return rerr
			}
			return fmt.Errorf("ops queue: %w", err)
		}
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			return opsview.RenderNoDaemon(os.Stdout, format)
		}
		return fmt.Errorf("ops queue: %w", err)
	}
	return opsview.RenderLocalQueue(os.Stdout, qs, opsview.Serving(), format)
}

func runOpsStats(args []string) error {
	fs := flag.NewFlagSet("ops stats", flag.ContinueOnError)
	getOut := opsOutputFlags(fs)
	home := fs.String("home", "", "sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	format := resolveOpsFormat(getOut())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: *home, Version: sparkwingModuleVersion()})
	if err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			return opsview.RenderStats(os.Stdout, wingwire.QueueState{}, format)
		}
		return fmt.Errorf("ops stats: %w", err)
	}
	return opsview.RenderStats(os.Stdout, qs, format)
}

func runOpsStatsReset(args []string) error {
	fs := flag.NewFlagSet("ops stats-reset", flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wingdclient.ResetStats(ctx, wingdclient.Options{Home: *home, Version: sparkwingModuleVersion()}); err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			fmt.Fprintln(os.Stdout, "no admission daemon running; nothing to reset")
			return nil
		}
		return fmt.Errorf("ops stats-reset: %w", err)
	}
	fmt.Fprintln(os.Stdout, "admission stats reset")
	return nil
}

func runOpsDoctor(args []string) error {
	fs := flag.NewFlagSet("ops doctor", flag.ContinueOnError)
	getOut := opsOutputFlags(fs)
	home := fs.String("home", "", "sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)")
	dryRun := fs.Bool("dry-run", false, "report what would be repaired without changing anything")
	if err := fs.Parse(args); err != nil {
		return err
	}
	format := resolveOpsFormat(getOut())
	p := PathsAt(*home)
	if *home == "" {
		var err error
		p, err = DefaultPaths()
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := opsview.Diagnose(ctx, p, *home, sparkwingModuleVersion(), *dryRun)
	if err != nil {
		diagnoseErr := fmt.Errorf("ops doctor: %w", err)
		if len(report.PermissionRepairs) == 0 && !report.PermissionAuditUnverified {
			return diagnoseErr
		}
		return errors.Join(
			opsview.RenderDoctor(os.Stdout, report, format, opsLegacyWarningLine(len(report.LiveLegacyHolders))),
			diagnoseErr,
		)
	}
	return opsview.RenderDoctor(os.Stdout, report, format, opsLegacyWarningLine(len(report.LiveLegacyHolders)))
}

func runOpsVersion(args []string) error {
	fs := flag.NewFlagSet("ops version", flag.ContinueOnError)
	getOut := opsOutputFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	v := sparkwingModuleVersion()
	if resolveOpsFormat(getOut()) == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]string{"version": v})
	}
	fmt.Fprintln(os.Stdout, v)
	return nil
}

func opsLegacyWarningLine(n int) string {
	if n <= 0 {
		return ""
	}
	noun := "pipeline"
	if n != 1 {
		noun = "pipelines"
	}
	return fmt.Sprintf(
		"%d legacy-pinned %s running outside daemon admission -- bump their sparkwing pins",
		n, noun)
}
