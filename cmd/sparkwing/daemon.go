package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/color"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

func explainRunAdmission(out io.Writer, records []journal.Record, runID, format string) error {
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
			if err := json.NewEncoder(out).Encode(r); err != nil {
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
		if _, err := fmt.Fprintf(out, "%s  %s%s\n", r.TS.Local().Format("2006-01-02 15:04:05"), label, explainEvent(r)); err != nil {
			return err
		}
	}
	if !found && format != "json" {
		_, err := fmt.Fprintf(out, "No retained admission events for %s.\n", runID)
		return err
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

func runDaemonRecoverState(inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	yes := inv.Bool("yes")
	if !yes {
		return errors.New("daemon recover-state: refusing without --yes; unreadable state may describe live admission holders")
	}
	quarantined, err := wingd.RecoverUnreadableState("", time.Now())
	if err != nil {
		return fmt.Errorf("daemon recover-state: %w", err)
	}
	if inv.Output(color.IsInteractiveStdout()) == "json" {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Preserved string `json:"preserved"`
		}{quarantined})
	}
	_, err = fmt.Fprintln(os.Stdout, quarantined)
	return err
}
