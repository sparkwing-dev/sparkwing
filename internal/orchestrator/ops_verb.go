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

// RunOps serves the `ops` subcommand for any compiled pipeline binary. A host
// running only a pinned pipeline binary -- no sparkwing CLI installed -- still
// reaches the local admission daemon's operational surfaces through it:
// everything the CLI shows and repairs at runtime, the pipeline binary can do
// for itself. Rendering is shared with the CLI through internal/opsview.
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

// opsOutputFlags registers -o and --output on fs and returns an accessor that
// prefers the short form, so operators can use either spelling.
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

// resolveOpsFormat picks the output format: an explicit choice wins; otherwise
// pretty on a terminal and json when piped, matching the CLI's convention.
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
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: *home})
	if err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			return opsview.RenderNoDaemon(os.Stdout, format)
		}
		return fmt.Errorf("ops queue: %w", err)
	}
	return opsview.RenderQueue(os.Stdout, qs, format)
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
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: *home})
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
	if err := wingdclient.ResetStats(ctx, wingdclient.Options{Home: *home}); err != nil {
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
		return fmt.Errorf("ops doctor: %w", err)
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

// opsLegacyWarningLine renders the coexistence warning shown by ops doctor
// when older-pinned binaries admit outside the daemon. Empty when none are
// live.
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
