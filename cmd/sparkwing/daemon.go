package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the last of the nesting shutdown windows on wingd.FinalizeDrainWindow.
// A restart outlasts the supervisor's termination grace, or it reports on a
// daemon that is still stopping.
const daemonRestartTimeout = 20 * time.Second

type daemonReport struct {
	Running             bool     `json:"running"`
	Healthy             bool     `json:"healthy"`
	Draining            bool     `json:"draining"`
	Restarted           bool     `json:"restarted"`
	BinaryVersion       string   `json:"binary_version,omitempty"`
	RunningRevision     string   `json:"running_revision,omitempty"`
	PreviousVersion     string   `json:"previous_version,omitempty"`
	PreviousRevision    string   `json:"previous_revision,omitempty"`
	Socket              string   `json:"socket"`
	APISocket           string   `json:"api_socket"`
	APIReady            bool     `json:"api_ready"`
	InstalledVersion    string   `json:"installed_version,omitempty"`
	DaemonSchemaVersion int      `json:"daemon_schema_version,omitempty"`
	StoreSchemaVersion  int      `json:"store_schema_version,omitempty"`
	StoreSchemaError    string   `json:"store_schema_error,omitempty"`
	DaemonStoreReady    *bool    `json:"daemon_store_ready,omitempty"`
	DaemonStoreError    string   `json:"daemon_store_error,omitempty"`
	StorePath           string   `json:"store_path,omitempty"`
	SchemaDiverged      bool     `json:"schema_diverged,omitempty"`
	MissingRequirements []string `json:"missing_requirements,omitempty"`
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
	apiSocket, err := wingd.APISocketPath(home)
	if err != nil {
		return daemonReport{}, err
	}
	report := daemonReport{
		Socket:           socket,
		APISocket:        apiSocket,
		InstalledVersion: installedVersion(),
		StorePath:        storeDBPath(home),
	}
	version, storeRequirements, schemaErr := storeSchemaState(ctx, home)
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
	report.DaemonStoreReady = info.StoreReady
	report.DaemonStoreError = info.StoreError
	report.APIReady = info.APIReady != nil && *info.APIReady
	report.MissingRequirements = store.MissingRequirements(info.StoreRequirements, storeRequirements)
	report.SchemaDiverged = daemonCannotReadStore(report, info.StoreRequirements)
	if report.SchemaDiverged || report.StoreSchemaError != "" || daemonStoreUnusable(report) {
		report.Healthy = false
	}
	return report, nil
}

// safety: a daemon that has not met the store yet reports the same "not ready" as
// one that cannot open it, so the daemon's own reason is a fault only when this
// home has a store to open.
func daemonStoreUnusable(report daemonReport) bool {
	if report.DaemonStoreError == "" {
		return false
	}
	return report.StoreSchemaVersion > 0 || report.StoreSchemaError != ""
}

// safety: a store schema above the daemon's own no longer proves the daemon cannot
// read it, so the version comparison holds only for a daemon too old to advertise
// requirements.
func daemonCannotReadStore(report daemonReport, daemonRequirements []string) bool {
	if report.DaemonSchemaVersion == 0 {
		return false
	}
	if daemonRequirements != nil {
		return len(report.MissingRequirements) > 0
	}
	return report.StoreSchemaVersion > report.DaemonSchemaVersion
}

func storeDBPath(home string) string {
	root, err := wingd.HomeDir(home)
	if err != nil || root == "" {
		return ""
	}
	return paths.PathsAt(root).StateDB()
}

// safety: an absent store is 0 with no error, but a store that exists and
// cannot be read must be an error. Folding the two together reported an
// unreadable store as a healthy daemon.
func storeSchemaState(ctx context.Context, home string) (int, []string, error) {
	root, err := wingd.HomeDir(home)
	if err != nil {
		return 0, nil, fmt.Errorf("resolve the sparkwing home: %w", err)
	}
	if root == "" {
		return 0, nil, errors.New("the sparkwing home did not resolve to a directory")
	}
	db := paths.PathsAt(root).StateDB()
	if _, err := os.Stat(db); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("stat the runs store: %w", err)
	}
	st, err := store.OpenReadOnly(db)
	if err != nil {
		return 0, nil, fmt.Errorf("open the runs store: %w", err)
	}
	defer func() { _ = st.Close() }()
	version, err := st.CurrentSchemaVersion(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("read the runs-store schema: %w", err)
	}
	requirements, err := st.Requirements(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("read the runs-store requirements: %w", err)
	}
	return version, requirements, nil
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
	ctx, cancel := context.WithTimeout(context.Background(), daemonRestartTimeout)
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
			"the installed sparkwing is the same build (%s), so `sparkwing daemon restart` will not help; install a sparkwing that understands %s, or set %s to a binary that does and stop the daemon",
			report.InstalledVersion, schemaShortfall(report), wingdclient.HostBinEnv)
	}
	return fmt.Sprintf("run `sparkwing daemon restart` to replace it with the installed %s", report.InstalledVersion)
}

func storeRemedy(report daemonReport) string {
	if report.StorePath == "" {
		return "run `sparkwing doctor` to inspect this home's runs store"
	}
	return fmt.Sprintf("inspect %s, then run `sparkwing doctor`", report.StorePath)
}

func schemaShortfall(report daemonReport) string {
	if len(report.MissingRequirements) > 0 {
		return strings.Join(report.MissingRequirements, ", ")
	}
	return fmt.Sprintf("schema %d", report.StoreSchemaVersion)
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
		if report.APIReady {
			fmt.Fprintf(os.Stdout, "controller API on %s\n", report.APISocket)
		} else {
			fmt.Fprintf(os.Stdout, "controller API not served on %s\n", report.APISocket)
		}
		if report.StoreSchemaError != "" {
			fmt.Fprintf(os.Stdout, "runs store unreadable: %s\n", report.StoreSchemaError)
		}
		if daemonStoreUnusable(report) {
			fmt.Fprintf(os.Stdout, "the daemon cannot use the runs store: %s\n", report.DaemonStoreError)
			if !report.SchemaDiverged {
				fmt.Fprintln(os.Stdout, storeRemedy(report))
			}
		}
		if report.SchemaDiverged {
			if len(report.MissingRequirements) > 0 {
				fmt.Fprintf(os.Stdout,
					"runs-store mismatch: the store uses %s, which the daemon does not understand, so it refuses every run\n",
					strings.Join(report.MissingRequirements, ", "))
			} else {
				fmt.Fprintf(os.Stdout,
					"runs-store schema mismatch: the daemon understands %d, the store is at %d, so it refuses every run\n",
					report.DaemonSchemaVersion, report.StoreSchemaVersion)
			}
			fmt.Fprintln(os.Stdout, schemaRemedy(report))
		}
		return nil
	default:
		return fmt.Errorf("daemon: unsupported output format %q", output)
	}
}
