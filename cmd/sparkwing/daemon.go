package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

func runDaemon(args []string) error {
	if handleParentHelp(cmdDaemon, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdDaemon, os.Stdout)
		return nil
	}
	switch args[0] {
	case "status", "restart", "stop":
		return runDaemonLifecycle(args)
	case "recover-state":
		return runDaemonRecoverState(args[1:])
	case "events":
		return runDaemonEvents(args[1:])
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
	run := fs.String("run", "", "run ID")
	since := fs.Duration("since", 0, "lookback duration")
	kinds := fs.StringArray("kind", nil, "record kind (repeatable)")
	incarnation := fs.Int64("incarnation", 0, "daemon incarnation")
	limit := fs.Int("limit", 50, "maximum records (0 for all)")
	offset := fs.Int("offset", 0, "matching records to skip from newest")
	output := fs.StringP("output", "o", "", "output format: pretty|json|plain (default: pretty on TTY, json when piped)")
	explain := fs.Bool("explain", false, "with --run, explain the run's admission history in sentences")
	if err := parseAndCheck(cmdDaemonEvents, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *explain {
		for _, name := range []string{"since", "kind", "incarnation", "limit", "offset"} {
			if fs.Changed(name) {
				return fmt.Errorf("daemon events --explain: --%s filters the event list; --explain reads every record of the run", name)
			}
		}
		if *run == "" {
			return errors.New("daemon events --explain: --run is required")
		}
		return explainRunAdmission(*run, *output)
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
	records, skipped, err := daemonJournal("")
	if err != nil {
		return err
	}
	reportSkippedJournalRecords(skipped)
	if len(records) == 0 && format != "json" {
		dir, err := wingd.StateDir("")
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
		if len(*kinds) > 0 && !slices.Contains(*kinds, r.Kind) {
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
			if err := printJournalJSON(r); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(os.Stdout, "%s  #%d.%d  %-22s %s %s\n", r.TS.Format(time.RFC3339Nano), r.Incarnation, r.Seq, r.Kind, r.RunID, compactData(r.Data))
		}
	}
	if start > 0 {
		fmt.Fprintf(os.Stderr, "Older events retained; rerun with the same filters and --offset %d (or --limit 0 for all).\n", *offset+end-start)
	}
	return nil
}

func explainRunAdmission(runID, output string) error {
	format, err := resolveTTYAwareOutput(output, cmdDaemonEvents.Path)
	if err != nil {
		return err
	}
	records, skipped, err := daemonJournal("")
	if err != nil {
		return err
	}
	reportSkippedJournalRecords(skipped)
	children := make(map[string][]string)
	related := map[string]bool{runID: true}
	for _, r := range records {
		if r.DisplayRunID == runID && r.RunID != "" {
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
		if !related[r.RunID] && r.DisplayRunID != runID {
			continue
		}
		found = true
		if format == "json" {
			if err := printJournalJSON(r); err != nil {
				return err
			}
			continue
		}
		label := ""
		if r.RunID != "" && r.RunID != runID {
			name := r.RunID
			if shown := display[r.RunID]; shown != "" {
				name = strings.TrimPrefix(shown, runID+"/")
			}
			label = name + ": "
		}
		fmt.Fprintf(os.Stdout, "%s  %s%s\n", r.TS.Local().Format("2006-01-02 15:04:05"), label, explainEvent(r))
	}
	if !found && format != "json" {
		fmt.Fprintf(os.Stdout, "No retained admission events for %s.\n", runID)
	}
	return nil
}

func printJournalJSON(r journal.Record) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(body))
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
		if r.Incarnation >= 1_000_000_000_000_000 {
			return "Reattached after daemon replacement"
		}
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
	quarantined, err := wingd.RecoverUnreadableState("", time.Now())
	if err != nil {
		return fmt.Errorf("daemon recover-state: %w", err)
	}
	fmt.Fprintf(os.Stdout, "preserved unreadable daemon state at %s\n", quarantined)
	return nil
}
