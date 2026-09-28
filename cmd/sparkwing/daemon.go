package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the last of the nesting shutdown windows on wingd.FinalizeDrainWindow.
// A restart outlasts the supervisor's termination grace, or it reports on a
// daemon that is still stopping.
const daemonRestartTimeout = 20 * time.Second

// safety: a stop waits out the same nesting shutdown windows a restart does,
// plus the supervisor's own exit, so it reports a daemon that is still
// stopping rather than one it merely stopped watching.
const daemonStopTimeout = 30 * time.Second

type daemonReport struct {
	Running             bool     `json:"running"`
	Healthy             bool     `json:"healthy"`
	Draining            bool     `json:"draining"`
	Restarted           bool     `json:"restarted"`
	Stopped             bool     `json:"stopped"`
	BinaryVersion       string   `json:"binary_version,omitempty"`
	RunningRevision     string   `json:"running_revision,omitempty"`
	PreviousVersion     string   `json:"previous_version,omitempty"`
	PreviousRevision    string   `json:"previous_revision,omitempty"`
	Socket              string   `json:"socket"`
	APISocket           string   `json:"api_socket"`
	APIReady            *bool    `json:"api_ready,omitempty"`
	APIError            string   `json:"api_error,omitempty"`
	ArtifactStoreError  string   `json:"artifact_store_error,omitempty"`
	InstalledVersion    string   `json:"installed_version,omitempty"`
	DaemonSchemaVersion int      `json:"daemon_schema_version,omitempty"`
	StoreSchemaVersion  int      `json:"store_schema_version,omitempty"`
	StoreSchemaError    string   `json:"store_schema_error,omitempty"`
	DaemonStoreReady    *bool    `json:"daemon_store_ready,omitempty"`
	DaemonStoreError    string   `json:"daemon_store_error,omitempty"`
	DaemonStoreSkew     bool     `json:"daemon_store_skew,omitempty"`
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
	case "stop":
		return runDaemonStop(args[1:])
	case "recover-state":
		return runDaemonRecoverState(args[1:])
	case "events":
		return runDaemonEvents(args[1:])
	case "explain":
		return runDaemonExplain(args[1:])
	default:
		PrintHelp(cmdDaemon, os.Stderr)
		return fmt.Errorf("daemon: unknown subcommand %q", args[0])
	}
}

func daemonJournal(home string) ([]journal.Record, int, error) {
	dir, err := wingd.StateDir(home)
	if err != nil {
		return nil, 0, err
	}
	return journal.ReadWithStats(dir)
}

func reportSkippedJournalRecords(skipped int) {
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "Skipped %d unreadable journal records.\n", skipped)
	}
}

func runDaemonEvents(args []string) error {
	fs := flag.NewFlagSet(cmdDaemonEvents.Path, flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home to inspect")
	run := fs.String("run", "", "run ID")
	since := fs.Duration("since", 0, "lookback duration")
	kinds := fs.StringArray("kind", nil, "record kind (repeatable)")
	incarnation := fs.Int64("incarnation", 0, "daemon incarnation")
	limit := fs.Int("limit", 50, "maximum records (0 for all)")
	offset := fs.Int("offset", 0, "matching records to skip from newest")
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")
	if err := parseAndCheck(cmdDaemonEvents, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *since < 0 {
		return errors.New("daemon events: --since must be nonnegative")
	}
	if *incarnation < 0 {
		return errors.New("daemon events: --incarnation must be nonnegative")
	}
	if *limit < 0 || *offset < 0 {
		return errors.New("daemon events: --limit and --offset must be nonnegative")
	}
	format, err := resolveTTYAwareOutput(*output, cmdDaemonEvents.Path)
	if err != nil {
		return err
	}
	records, skipped, err := daemonJournal(*home)
	if err != nil {
		return err
	}
	reportSkippedJournalRecords(skipped)
	if len(records) == 0 && format != "json" {
		dir, err := wingd.StateDir(*home)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "No events are retained in %s.\n", dir)
		return nil
	}
	cutoff := time.Now().Add(-*since)
	selected := make([]journal.Record, 0, len(records))
	for _, r := range records {
		if *run != "" && r.RunID != *run && r.DisplayRunID != *run {
			continue
		}
		if *since > 0 && r.TS.Before(cutoff) {
			continue
		}
		if *incarnation != 0 && r.Incarnation != uint64(*incarnation) {
			continue
		}
		if len(*kinds) > 0 && !containsString(*kinds, r.Kind) {
			continue
		}
		selected = append(selected, r)
	}
	end := max(0, len(selected)-*offset)
	start := 0
	if *limit > 0 {
		start = max(0, end-*limit)
	}
	for _, r := range selected[start:end] {
		if format == "json" {
			body, err := json.Marshal(r)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(body))
		} else {
			fmt.Fprintf(os.Stdout, "%s  #%d.%d  %-22s %s %s\n", r.TS.Format(time.RFC3339Nano), r.Incarnation, r.Seq, r.Kind, r.RunID, compactData(r.Data))
		}
	}
	if start > 0 {
		fmt.Fprintf(os.Stderr, "Older events retained; rerun with the same filters and --offset %d (or --limit 0 for all).\n", *offset+end-start)
	}
	return nil
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func runDaemonExplain(args []string) error {
	fs := flag.NewFlagSet(cmdDaemonExplain.Path, flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home to inspect")
	run := fs.String("run", "", "run ID to explain")
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")
	if err := parseAndCheck(cmdDaemonExplain, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *run == "" {
		return errors.New("daemon explain: --run is required")
	}
	format, err := resolveTTYAwareOutput(*output, cmdDaemonExplain.Path)
	if err != nil {
		return err
	}
	records, skipped, err := daemonJournal(*home)
	if err != nil {
		return err
	}
	reportSkippedJournalRecords(skipped)
	children := make(map[string][]string)
	related := map[string]bool{*run: true}
	for _, r := range records {
		if r.DisplayRunID == *run && r.RunID != "" {
			related[r.RunID] = true
		}
		if r.RunID == "" {
			continue
		}
		for _, field := range []string{"owner_run_id", "requested_owner_run_id", "requested_parent", "resolved_parent"} {
			owner, _ := r.Data[field].(string)
			if owner != "" {
				children[owner] = append(children[owner], r.RunID)
			}
		}
	}
	queue := make([]string, 0, len(related))
	for id := range related {
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		owner := queue[0]
		queue = queue[1:]
		for _, child := range children[owner] {
			if !related[child] {
				related[child] = true
				queue = append(queue, child)
			}
		}
	}
	display := map[string]string{}
	for _, r := range records {
		if r.RunID != "" && r.DisplayRunID != "" {
			display[r.RunID] = r.DisplayRunID
		}
	}
	found := false
	for _, r := range records {
		if !related[r.RunID] && r.DisplayRunID != *run {
			continue
		}
		found = true
		if format == "json" {
			body, err := json.Marshal(r)
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stdout, string(body))
			continue
		}
		label := ""
		if r.RunID != "" && r.RunID != *run {
			name := r.RunID
			if shown := display[r.RunID]; shown != "" {
				name = strings.TrimPrefix(shown, *run+"/")
			}
			label = name + ": "
		}
		fmt.Fprintf(os.Stdout, "%s  %s%s\n", r.TS.Local().Format("2006-01-02 15:04:05"), label, explainEvent(r))
	}
	if !found && format != "json" {
		fmt.Fprintf(os.Stdout, "No retained admission events for %s.\n", *run)
	}
	return nil
}

func explainEvent(r journal.Record) string {
	field := func(name string) string { return compactValue(r.Data[name]) }
	peer := "an unknown peer"
	if r.PID > 0 {
		peer = fmt.Sprintf("pid %d", r.PID)
	}
	switch r.Kind {
	case "request":
		if r.Data["child_attach"] == true {
			return "Requested child attachment"
		}
		resources, _ := r.Data["resources"].(map[string]any)
		cores, _ := resources["cores"].(float64)
		memory, _ := resources["memory_bytes"].(float64)
		class := field("class")
		if class == "" {
			class = "normal"
		}
		var parts []string
		if cores > 0 {
			parts = append(parts, fmt.Sprintf("%g cores", cores))
		}
		if memory > 0 {
			parts = append(parts, humanBytes(int64(memory)))
		}
		if r.Data["semaphores"] != nil {
			parts = append(parts, "semaphore slots")
		}
		if len(parts) == 0 {
			return "Requested admission (" + class + ", priority " + field("priority") + ")"
		}
		return "Requested " + strings.Join(parts, " and ") + " (" + class + ", priority " + field("priority") + ")"
	case "queued":
		if blocker := field("blocker"); blocker != "" {
			return "Queued behind run " + blocker + ": " + field("blocking_reason")
		}
		return "Queued at position " + field("position") + ": " + field("blocking_reason")
	case "grant":
		wait, _ := r.Data["wait_ms"].(float64)
		if r.Data["backfill"] == true {
			return fmt.Sprintf("Admitted after %s (backfill)", time.Duration(wait)*time.Millisecond)
		}
		return fmt.Sprintf("Admitted after %s", time.Duration(wait)*time.Millisecond)
	case "connection_opened":
		return "Connection opened by " + peer
	case "connection_handshake":
		return "Connection handshake completed with " + peer
	case "connection_closed":
		return "Connection closed for " + peer + " (" + field("role") + ")"
	case "child_attach_request":
		return "Requested child attachment to " + field("requested_parent")
	case "reattach_accepted":
		return fmt.Sprintf("Reattached after daemon replacement (incarnation %d)", r.Incarnation)
	case "reattach_request":
		return "Requested lease reattachment"
	case "reattach_refused":
		return "Reattachment refused: " + field("reason")
	case "child_attach":
		return "Attached child to run " + field("resolved_parent")
	case "cancel":
		requester := "an unknown peer"
		if known, _ := r.Data["pid_known"].(bool); known {
			requester = "pid " + field("requesting_pid")
		}
		var others []string
		if runs, ok := r.Data["affected_runs"].([]any); ok {
			for _, run := range runs {
				if name := compactValue(run); name != r.RunID {
					others = append(others, name)
				}
			}
		}
		if len(others) > 0 {
			return "Cancelled by " + requester + " (also cancelled: " + strings.Join(others, ", ") + ")"
		}
		return "Cancelled by " + requester
	case "release":
		return "Released " + field("lease_id")
	case "superseded":
		return "Superseded by run " + field("by_run") + ": " + field("reason")
	case "denied":
		return "Admission denied: " + field("reason")
	case "rejected":
		return "Request rejected: " + field("reason")
	case "rejection":
		return "Invalid request rejected: " + field("reason")
	case "eviction":
		return "Evicted: " + field("reason")
	case "queue_timeout":
		return "Queue wait expired: " + field("reason")
	case "cancellation":
		return "Admission cancelled: " + field("reason")
	case "backfill":
		return "Backfilled past an older request"
	case "reprioritize":
		return "Priority changed to " + field("priority")
	case "contended":
		return "Ran under contention"
	case "grace_expiry":
		return "Reattachment grace expired for " + field("lease_id")
	case "start":
		return "Daemon started (version " + field("version") + ")"
	case "ready":
		return "Daemon ready at " + field("socket")
	case "shutdown":
		return "Daemon stopped: " + field("reason")
	case "drain_begin":
		return "Daemon began draining for " + field("successor_version")
	case "drain_end":
		return "Daemon finished draining"
	case "headroom_sample":
		return "Available capacity sampled: " + field("target_cores") + " cores"
	case "probe_failure_start":
		return "Supervisor probe failures began: " + field("error")
	case "probe_failure_end":
		return "Supervisor probe failures ended after " + field("duration_ms") + "ms"
	case "replacement":
		return "Supervisor replaced the daemon after " + field("failed_probes") + " failed probes"
	case "handshake_refused":
		reason := field("reason")
		if reason == "" {
			reason = "unsupported message " + field("message_type")
		}
		return "Connection handshake refused: " + reason
	case "message_refused":
		return "Message refused: " + field("message_type")
	case "dropped":
		return "Journal dropped " + field("count") + " records"
	default:
		if len(r.Data) == 0 {
			return r.Kind
		}
		return r.Kind + ": " + compactData(r.Data)
	}
}

func compactData(data map[string]any) string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+compactValue(data[key]))
	}
	return strings.Join(parts, " ")
}

func compactValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case map[string]any:
		return "(" + compactData(v) + ")"
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, compactValue(item))
		}
		return strings.Join(parts, ", ")
	case []string:
		return strings.Join(v, ", ")
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func runDaemonRecoverState(args []string) error {
	fs := flag.NewFlagSet(cmdDaemonRecoverState.Path, flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home whose unreadable daemon state should be preserved")
	yes := fs.Bool("yes", false, "confirm every run described by the unreadable state has stopped")
	if err := parseAndCheck(cmdDaemonRecoverState, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if !*yes {
		return errors.New("daemon recover-state: refusing without --yes; unreadable state may describe live admission holders")
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
	report.APIReady = info.APIReady
	report.APIError = info.APIError
	report.ArtifactStoreError = info.ArtifactStoreError
	report.MissingRequirements = store.MissingRequirements(info.StoreRequirements, storeRequirements)
	report.SchemaDiverged = daemonCannotReadStore(report, info.StoreRequirements)
	report.DaemonStoreSkew = daemonStoreSkewed(report)
	if report.SchemaDiverged || report.StoreSchemaError != "" || daemonStoreUnusable(report) || daemonAPIUnusable(report) {
		report.Healthy = false
	}
	return report, nil
}

// safety: a daemon whose API socket is unbound arbitrates admission and
// serves no run state, so it answers a probe like a healthy daemon while
// every hosted run fails; only a daemon that reports the field at all can be
// judged on it.
func daemonAPIUnusable(report daemonReport) bool {
	return report.APIReady != nil && !*report.APIReady && !report.Draining
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

// safety: a store the daemon is merely too old to open is age, and age moves a
// run to the standalone store rather than failing it, so an operator reading
// this must not be sent looking for a corrupt file.
func daemonStoreSkewed(report daemonReport) bool {
	return report.DaemonStoreError != "" && len(report.MissingRequirements) > 0
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

func runDaemonStop(args []string) error {
	return runDaemonStopWith(args, daemonStopDeps{
		stop:    wingdclient.Stop,
		inspect: inspectDaemon,
	})
}

type daemonStopDeps struct {
	stop    func(context.Context, wingdclient.Options) error
	inspect func(context.Context, string) (daemonReport, error)
}

func runDaemonStopWith(args []string, deps daemonStopDeps) error {
	fs := flag.NewFlagSet(cmdDaemonStop.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")
	home := fs.String("home", "", "sparkwing home whose daemon should stop")
	if err := parseAndCheck(cmdDaemonStop, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonStopTimeout)
	defer cancel()
	format, err := resolveTTYAwareOutput(*output, cmdDaemonStop.Path)
	if err != nil {
		return err
	}
	before, err := deps.inspect(ctx, *home)
	if err != nil {
		return err
	}
	if !before.Running {
		return emitDaemonReport(before, format)
	}
	stopErr := deps.stop(ctx, wingdclient.Options{
		Home:    *home,
		Version: installedVersion(),
		Logf:    func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	if stopErr != nil && !errors.Is(stopErr, wingdclient.ErrNoDaemon) {
		return fmt.Errorf("daemon stop: %w", stopErr)
	}
	report, err := deps.inspect(ctx, *home)
	if err != nil {
		return err
	}
	if report.Running {
		return fmt.Errorf("daemon stop: %s still answers after %s; run `sparkwing daemon status` for what it reports",
			report.BinaryVersion, daemonStopTimeout)
	}
	report.Stopped = true
	report.PreviousVersion = before.BinaryVersion
	report.PreviousRevision = versionRevision(before.BinaryVersion)
	return emitDaemonReport(report, format)
}

func apiFault(report daemonReport) string {
	if report.APIError != "" {
		return report.APIError
	}
	return "the daemon did not say why"
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
		return enc.Encode(report)
	case "plain":
		if !report.Running {
			fmt.Fprintln(os.Stdout, "stopped")
			return nil
		}
		fmt.Fprintln(os.Stdout, report.BinaryVersion)
		return nil
	case "pretty", "":
		if report.Stopped {
			was := ""
			if report.PreviousVersion != "" {
				was = " (was " + report.PreviousVersion + ")"
			}
			fmt.Fprintf(os.Stdout, "wingd is stopped%s\n", was)
			return nil
		}
		if !report.Running {
			fmt.Fprintln(os.Stdout, "wingd is stopped")
			return nil
		}
		action := "running"
		if report.Restarted {
			action = "restarted"
		}
		fmt.Fprintf(os.Stdout, "wingd %s %s\n", action, report.BinaryVersion)
		switch {
		case report.APIReady == nil:
		case *report.APIReady:
			fmt.Fprintf(os.Stdout, "controller API on %s\n", report.APISocket)
		default:
			fmt.Fprintf(os.Stdout, "controller API not served: %s\n", apiFault(report))
		}
		if report.ArtifactStoreError != "" {
			fmt.Fprintf(os.Stdout, "no artifact routes: %s\n", report.ArtifactStoreError)
		}
		if report.StoreSchemaError != "" {
			fmt.Fprintf(os.Stdout, "runs store unreadable: %s\n", report.StoreSchemaError)
		}
		if daemonStoreUnusable(report) {
			verb := "cannot use"
			if report.DaemonStoreSkew {
				verb = "is behind"
			}
			fmt.Fprintf(os.Stdout, "the daemon %s the runs store: %s\n", verb, report.DaemonStoreError)
			if !report.SchemaDiverged {
				fmt.Fprintln(os.Stdout, storeRemedy(report))
			}
		}
		if report.SchemaDiverged {
			if len(report.MissingRequirements) > 0 {
				fmt.Fprintf(os.Stdout,
					"runs-store mismatch: the store uses %s, which the daemon does not understand, so every run goes standalone\n",
					strings.Join(report.MissingRequirements, ", "))
			} else {
				fmt.Fprintf(os.Stdout,
					"runs-store schema mismatch: the daemon understands %d, the store is at %d, so every run goes standalone\n",
					report.DaemonSchemaVersion, report.StoreSchemaVersion)
			}
			fmt.Fprintln(os.Stdout, schemaRemedy(report))
		}
		return nil
	default:
		return fmt.Errorf("daemon: unsupported output format %q", output)
	}
}
