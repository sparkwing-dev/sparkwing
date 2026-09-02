package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type daemonReport struct {
	Running             bool   `json:"running"`
	Healthy             bool   `json:"healthy"`
	Draining            bool   `json:"draining"`
	Restarted           bool   `json:"restarted"`
	BinaryVersion       string `json:"binary_version,omitempty"`
	RunningRevision     string `json:"running_revision,omitempty"`
	PreviousVersion     string `json:"previous_version,omitempty"`
	PreviousRevision    string `json:"previous_revision,omitempty"`
	Socket              string `json:"socket"`
	InstalledVersion    string `json:"installed_version,omitempty"`
	DaemonSchemaVersion int    `json:"daemon_schema_version,omitempty"`
	StoreSchemaVersion  int    `json:"store_schema_version,omitempty"`
	StoreSchemaError    string `json:"store_schema_error,omitempty"`
	SchemaDiverged      bool   `json:"schema_diverged,omitempty"`
}

func runDaemon(args []string) error {
	if handleParentHelp(cmdDaemon, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdDaemon, os.Stdout)
		return nil
	}
	switch args[0] {
	case "status":
		return runDaemonStatus(args[1:])
	case "restart":
		return runDaemonRestart(args[1:])
	case "recover-state":
		return runDaemonRecoverState(args[1:])
	default:
		PrintHelp(cmdDaemon, os.Stderr)
		return fmt.Errorf("daemon: unknown subcommand %q", args[0])
	}
}

func runDaemonRecoverState(args []string) error {
	fs := flag.NewFlagSet(cmdDaemonRecoverState.Path, flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home whose unreadable daemon state should be preserved")
	yes := fs.Bool("yes", false, "confirm every guarded command described by the unreadable state has stopped")
	if err := parseAndCheck(cmdDaemonRecoverState, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if !*yes {
		return errors.New("daemon recover-state: refusing without --yes; unreadable state may describe live guarded commands")
	}
	quarantined, err := wingd.RecoverUnreadableState(*home, time.Now())
	if err != nil {
		return fmt.Errorf("daemon recover-state: %w", err)
	}
	fmt.Fprintf(os.Stdout, "preserved unreadable daemon state at %s\n", quarantined)
	return nil
}

func runDaemonStatus(args []string) error {
	fs := flag.NewFlagSet(cmdDaemonStatus.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")
	home := fs.String("home", "", "sparkwing home to inspect")
	if err := parseAndCheck(cmdDaemonStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report, err := inspectDaemon(ctx, *home)
	if err != nil {
		return err
	}
	format, err := resolveTTYAwareOutput(*output, cmdDaemonStatus.Path)
	if err != nil {
		return err
	}
	return emitDaemonReport(report, format)
}

func inspectDaemon(ctx context.Context, home string) (daemonReport, error) {
	socket, err := wingd.SocketPath(home)
	if err != nil {
		return daemonReport{}, err
	}
	report := daemonReport{Socket: socket, InstalledVersion: installedVersion()}
	version, schemaErr := storeSchemaVersion(ctx, home)
	report.StoreSchemaVersion = version
	if schemaErr != nil {
		report.StoreSchemaError = schemaErr.Error()
	}
	info, err := wingdclient.Probe(ctx, socket)
	if errors.Is(err, wingdclient.ErrNoDaemon) {
		return report, nil
	}
	if err != nil {
		return daemonReport{}, fmt.Errorf("daemon status: %w", err)
	}
	report.Running = true
	report.Healthy = !info.Draining
	report.Draining = info.Draining
	report.BinaryVersion = info.BinaryVersion
	report.RunningRevision = versionRevision(info.BinaryVersion)
	report.DaemonSchemaVersion = info.StoreSchemaVersion
	report.SchemaDiverged = report.DaemonSchemaVersion > 0 &&
		report.StoreSchemaVersion > report.DaemonSchemaVersion
	if report.SchemaDiverged || report.StoreSchemaError != "" {
		report.Healthy = false
	}
	return report, nil
}

// safety: an absent store is 0 with no error, but a store that exists and
// cannot be read must be an error. Folding the two together reported an
// unreadable store as a healthy daemon.
func storeSchemaVersion(ctx context.Context, home string) (int, error) {
	root, err := wingd.HomeDir(home)
	if err != nil {
		return 0, fmt.Errorf("resolve the sparkwing home: %w", err)
	}
	if root == "" {
		return 0, errors.New("the sparkwing home did not resolve to a directory")
	}
	db := paths.PathsAt(root).StateDB()
	if _, err := os.Stat(db); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat the runs store: %w", err)
	}
	st, err := store.OpenReadOnly(db)
	if err != nil {
		return 0, fmt.Errorf("open the runs store: %w", err)
	}
	defer func() { _ = st.Close() }()
	version, err := st.CurrentSchemaVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("read the runs-store schema: %w", err)
	}
	return version, nil
}

func runDaemonRestart(args []string) error {
	return runDaemonRestartWith(args, daemonRestartDeps{
		installedVersion: installedVersion,
		refresh:          wingdclient.RefreshRunning,
		restart:          wingdclient.RestartRunning,
		inspect:          inspectDaemon,
	})
}

type daemonRestartDeps struct {
	installedVersion func() string
	refresh          func(context.Context, wingdclient.Options) (wingdclient.RefreshResult, error)
	restart          func(context.Context, wingdclient.Options) (wingdclient.RefreshResult, error)
	inspect          func(context.Context, string) (daemonReport, error)
}

func runDaemonRestartWith(args []string, deps daemonRestartDeps) error {
	fs := flag.NewFlagSet(cmdDaemonRestart.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")
	home := fs.String("home", "", "sparkwing home to refresh")
	force := fs.Bool("force", false, "replace the daemon even when it already serves this build")
	if err := parseAndCheck(cmdDaemonRestart, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	format, err := resolveTTYAwareOutput(*output, cmdDaemonRestart.Path)
	if err != nil {
		return err
	}
	target := deps.installedVersion()
	replace := deps.refresh
	if *force {
		replace = deps.restart
	}
	result, err := replace(ctx, wingdclient.Options{
		Home:    *home,
		Version: target,
		Logf:    func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	if errors.Is(err, wingdclient.ErrNoDaemon) {
		report, inspectErr := deps.inspect(ctx, *home)
		if inspectErr != nil {
			return inspectErr
		}
		return emitDaemonReport(report, format)
	}
	if err != nil {
		return fmt.Errorf("daemon restart: %w", err)
	}
	report, err := deps.inspect(ctx, *home)
	if err != nil {
		return err
	}
	report.Restarted = result.Restarted
	report.PreviousVersion = result.PreviousVersion
	report.PreviousRevision = versionRevision(result.PreviousVersion)
	return emitDaemonReport(report, format)
}

func schemaRemedy(report daemonReport) string {
	if report.InstalledVersion != "" && report.InstalledVersion == report.BinaryVersion {
		return fmt.Sprintf(
			"the installed sparkwing is the same build (%s), so `sparkwing daemon restart` will not help; install a sparkwing that understands schema %d, or set %s to a binary that does and stop the daemon",
			report.InstalledVersion, report.StoreSchemaVersion, wingdclient.HostBinEnv)
	}
	return fmt.Sprintf("run `sparkwing daemon restart` to replace it with the installed %s", report.InstalledVersion)
}

func emitDaemonReport(report daemonReport, output string) error {
	switch output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "plain":
		if !report.Running {
			fmt.Fprintln(os.Stdout, "stopped")
			return nil
		}
		fmt.Fprintln(os.Stdout, report.BinaryVersion)
		return nil
	case "pretty", "":
		if !report.Running {
			fmt.Fprintln(os.Stdout, "wingd is stopped")
			return nil
		}
		action := "running"
		if report.Restarted {
			action = "restarted"
		}
		fmt.Fprintf(os.Stdout, "wingd %s %s\n", action, report.BinaryVersion)
		if report.StoreSchemaError != "" {
			fmt.Fprintf(os.Stdout, "runs store unreadable: %s\n", report.StoreSchemaError)
		}
		if report.SchemaDiverged {
			fmt.Fprintf(os.Stdout,
				"runs-store schema mismatch: the daemon understands %d, the store is at %d, so it refuses every run\n",
				report.DaemonSchemaVersion, report.StoreSchemaVersion)
			fmt.Fprintln(os.Stdout, schemaRemedy(report))
		}
		return nil
	default:
		return fmt.Errorf("daemon: unsupported output format %q", output)
	}
}
