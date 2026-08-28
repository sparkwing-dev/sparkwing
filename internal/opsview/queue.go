package opsview

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func RenderQueue(w io.Writer, qs wingwire.QueueState, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(qs)
	case "plain":
		return renderQueuePlain(w, qs)
	default:
		return RenderQueuePretty(w, qs)
	}
}

const (
	ReachServing = "serving"

	ReachAbsent = "absent"

	ReachUnreachable = "unreachable"
)

type DaemonReach struct {
	Reachable bool   `json:"reachable"`
	State     string `json:"state"`

	Detail string `json:"detail,omitempty"`
}

func Serving() DaemonReach {
	return DaemonReach{Reachable: true, State: ReachServing}
}

func Absent() DaemonReach {
	return DaemonReach{State: ReachAbsent, Detail: "no admission daemon is running for this home, so nothing is queued"}
}

func Unreachable(cause error) DaemonReach {
	detail := "the admission daemon socket could not be reached"
	if cause != nil {
		detail = cause.Error()
	}
	return DaemonReach{State: ReachUnreachable, Detail: detail}
}

type queueView struct {
	wingwire.QueueState
	Daemon DaemonReach `json:"daemon"`
}

func RenderLocalQueue(w io.Writer, qs wingwire.QueueState, reach DaemonReach, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(queueView{QueueState: qs, Daemon: reach})
	case "plain":
		fmt.Fprintf(w, "daemon\t%s\n", reach.State)
		if reach.State != ReachServing {
			return nil
		}
		return renderQueuePlain(w, qs)
	default:
		switch reach.State {
		case ReachAbsent:
			fmt.Fprintln(w, "no admission daemon running; nothing is queued")
			return nil
		case ReachUnreachable:
			fmt.Fprintf(w, "cannot reach the admission daemon: %s\n", reach.Detail)
			fmt.Fprintln(w, "the local queue is unknown, not empty -- nothing here says whether runs are holding or waiting")
			return nil
		default:
			return RenderQueuePretty(w, qs)
		}
	}
}

func RenderNoDaemon(w io.Writer, format string) error {
	return RenderLocalQueue(w, wingwire.QueueState{}, Absent(), format)
}

func RenderUnreachableDaemon(w io.Writer, format string, cause error) error {
	return RenderLocalQueue(w, wingwire.QueueState{}, Unreachable(cause), format)
}

func renderQueuePlain(w io.Writer, qs wingwire.QueueState) error {
	for _, r := range qs.Resources {
		fmt.Fprintf(w, "resource\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Key,
			fmtAmount(r.Key, r.Capacity), fmtAmount(r.Key, r.Held),
			fmtHeadroomCell(r.Key, r.Reserved), externalCell(r),
			fmtAmount(r.Key, resourceAvailable(r)))
	}
	if c := qs.Container; c != nil {
		fmt.Fprintf(w, "container\t%.3f\t%.3f\t%d\t%d\n",
			c.Cores, c.HostCores, c.MemoryBytes, c.HostMemoryBytes)
	}
	if qs.Budget != nil {
		fmt.Fprintf(w, "budget\t%.3f\t%.3f\t%d\t%d\t%t\t%s\t%s\n",
			qs.Budget.Cores, qs.Budget.MachineCores,
			qs.Budget.MemoryBytes, qs.Budget.MachineMemoryBytes, qs.Budget.Enforce,
			orDash(qs.Budget.Source), orDash(qs.Budget.Origin))
	}
	if qs.IgnoreExternal {
		fmt.Fprintf(w, "external\tignored\t%s\n", orDash(budgetOrigin(qs.Budget)))
	}
	for _, r := range qs.Resources {
		if isHostResource(r.Key) && r.ExternalSource == wingwire.ExternalUnmeasured {
			fmt.Fprintf(w, "external\tunmeasured\t%s\n", r.Key)
		}
	}
	if qs.ExternalSampleAgeMS > 0 {
		fmt.Fprintf(w, "external-age\t%d\n", qs.ExternalSampleAgeMS)
	}
	if qs.ExternalMeasurementAgeMS > 0 {
		fmt.Fprintf(w, "external-measurement-age\t%d\n", qs.ExternalMeasurementAgeMS)
	}
	for _, h := range qs.Holders {
		kind := "holder"
		if h.ConnectionOnly {
			kind = "connected"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", kind,
			h.RunID, orDash(h.ParticipantID), queueDisplayRunID(h.RunID, h.DisplayRunID),
			orDash(h.Pipeline), orDash(h.Repo), orDash(OriginWord(h.Origin)),
			fmtElapsed(h.ElapsedMS), fmtHolderCost(h),
			orDash(h.CostSource), joinKeys(h.Semaphores), stalledWord(h), orDash(queueParentID(h)))
	}
	for _, wt := range qs.Waiters {
		fmt.Fprintf(w, "waiter\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			wt.Position, wt.RunID, orDash(wt.ParticipantID),
			queueDisplayRunID(wt.RunID, wt.DisplayRunID),
			orDash(wt.Pipeline), orDash(wt.Repo), orDash(OriginWord(wt.Origin)),
			fmtCost(wt.Resources), orDash(wt.CostSource), fmtETA(wt.ExpectedStartMS),
			joinKeys(wt.WaitingOn), fmtElapsed(wt.WaitingMS), orDash(wt.BlockingReason), wt.Priority)
	}
	for _, r := range qs.Runners {
		fmt.Fprintf(w, "runner\t%s\t%.3f\t%d\t%d\n", r.Name, r.Cores, r.MemoryBytes, r.QueueDepth)
	}
	return nil
}

func RenderQueuePretty(out io.Writer, qs wingwire.QueueState) error {
	holders := queueLifecycleHolders(qs)
	connections := queueLifecycleConnections(qs)
	clear := ""
	if qs.ExpectedClearMS != nil && *qs.ExpectedClearMS > 0 {
		clear = fmt.Sprintf("; clears in ~%s", fmtElapsed(*qs.ExpectedClearMS))
	}
	if d := FmtDaemonHeader(qs); d != "" {
		fmt.Fprintln(out, d)
	}
	if cc := FmtCapacityChange(qs.CapacityChange); cc != "" {
		fmt.Fprintln(out, cc)
	}
	fmt.Fprintf(out, "local admission: %d holding, %d connected, %d queued%s\n", len(holders), len(connections), len(qs.Waiters), clear)
	if line := FmtEventsLine(qs.Events); line != "" {
		fmt.Fprintln(out, line)
	}
	fmt.Fprintln(out)

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RESOURCE\tCAPACITY\tIN USE\tRESERVED\tEXTERNAL\tAVAILABLE")
	if len(qs.Resources) == 0 {
		fmt.Fprintln(tw, "(none)\t\t\t\t\t")
	}
	for _, r := range qs.Resources {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Key,
			fmtAmount(r.Key, r.Capacity), fmtAmount(r.Key, r.Held),
			fmtHeadroomCell(r.Key, r.Reserved), externalCell(r),
			fmtAmount(r.Key, resourceAvailable(r)))
	}
	_ = tw.Flush()
	if line := resourceLegend(qs); line != "" {
		fmt.Fprintln(out, line)
	}

	if c := ContainerNote(qs.Container); c != "" {
		fmt.Fprintf(out, "%s\n", c)
	}
	if b := BudgetNote(qs.Budget); b != "" {
		fmt.Fprintf(out, "%s\n", b)
	}
	if line := ExternalIgnoredNote(qs); line != "" {
		fmt.Fprintln(out, line)
	}
	if note := ExternalUnmeasuredNote(qs); note != "" {
		fmt.Fprintln(out, note)
	}
	if note := ExternalAgeNote(qs); note != "" {
		fmt.Fprintln(out, note)
	}
	if note := ExternalPressureNote(qs); note != "" {
		fmt.Fprintf(out, "\n%s\n", note)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Running")
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tPIPELINE\tREPO\tORIGIN\tELAPSED\tCOST\tSOURCE\tSEMAPHORES")
	if len(holders) == 0 {
		fmt.Fprintln(tw, "(none holding)\t\t\t\t\t\t\t")
	}
	for _, h := range holders {
		run := queueDisplayRunID(h.RunID, h.DisplayRunID)
		if h.Parent != "" {
			run = "  " + run + " (attached)"
		}
		if h.Stalled {
			run += " (stalled)"
		}
		if h.Contended {
			run += " (contended)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", run, orDash(h.Pipeline), orDash(h.Repo),
			orDash(OriginWord(h.Origin)), fmtElapsed(h.ElapsedMS), fmtHolderCost(h),
			orDash(h.CostSource), orDash(joinKeys(h.Semaphores)))
	}
	_ = tw.Flush()
	if len(connections) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Connected (no resources held)")
		tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "RUN\tPIPELINE\tREPO\tORIGIN\tELAPSED")
		for _, h := range connections {
			run := queueDisplayRunID(h.RunID, h.DisplayRunID)
			if h.Parent != "" {
				run = "  " + run + " (attached)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", run, orDash(h.Pipeline), orDash(h.Repo),
				orDash(OriginWord(h.Origin)), fmtElapsed(h.ElapsedMS))
		}
		_ = tw.Flush()
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Waiting")
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "POS\tPRI\tRUN\tPIPELINE\tREPO\tORIGIN\tCOST\tSOURCE\tETA\tWAITING ON\tWAITED")
	if len(qs.Waiters) == 0 {
		fmt.Fprintln(tw, "-\t-\t(no one queued)\t\t\t\t\t\t\t\t")
	}
	for _, wt := range qs.Waiters {
		run := queueDisplayRunID(wt.RunID, wt.DisplayRunID)
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", wt.Position, wt.Priority, run,
			orDash(wt.Pipeline), orDash(wt.Repo), orDash(OriginWord(wt.Origin)), fmtCost(wt.Resources),
			orDash(wt.CostSource), fmtETA(wt.ExpectedStartMS), orDash(joinKeys(wt.WaitingOn)),
			fmtElapsed(wt.WaitingMS))
	}
	_ = tw.Flush()

	for _, wt := range qs.Waiters {
		if wt.BlockingReason != "" {
			fmt.Fprintf(out, "\n%s waiting: %s\n", queueDisplayRunID(wt.RunID, wt.DisplayRunID), wt.BlockingReason)
		}
	}
	for _, h := range holders {
		if h.Stalled && h.Recovery != "" {
			fmt.Fprintf(out, "\n%s is stalled (idle while runs wait). Recover with:\n  %s\n", h.RunID, h.Recovery)
		}
	}
	for _, h := range holders {
		if h.Contended && h.ContentionReason != "" {
			fmt.Fprintf(out, "\n%s is contended (%s).\n", h.RunID, h.ContentionReason)
		}
	}
	for _, d := range queueDriftNotes(qs) {
		fmt.Fprintf(out, "\n%s: %s\n", d.runID, d.warning)
	}
	if len(qs.Runners) > 0 {
		fmt.Fprintln(out)
		tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "RUNNER\tFREE CORES\tFREE MEMORY\tQUEUE DEPTH")
		for _, r := range qs.Runners {
			mem := "-"
			if r.MemoryBytes > 0 {
				mem = humanBytes(r.MemoryBytes)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", r.Name, trimFloat(r.Cores), mem, r.QueueDepth)
		}
		_ = tw.Flush()
	}
	return nil
}

func queueLifecycleHolders(qs wingwire.QueueState) []wingwire.Holder {
	return queueLifecycleRows(qs, false)
}

func queueLifecycleConnections(qs wingwire.QueueState) []wingwire.Holder {
	return queueLifecycleRows(qs, true)
}

func queueLifecycleRows(qs wingwire.QueueState, connectionOnly bool) []wingwire.Holder {
	waitingOwners := make(map[string]bool)
	for _, w := range qs.Waiters {
		if w.ParticipantID != "" {
			waitingOwners[w.RunID] = true
		}
	}
	holders := make([]wingwire.Holder, 0, len(qs.Holders))
	for _, h := range qs.Holders {
		if h.ConnectionOnly != connectionOnly {
			continue
		}
		if connectionOnly {
			holders = append(holders, h)
			continue
		}
		orchestrationWait := h.AdmissionWaiting ||
			(h.ParticipantID == "" && h.Parent == "" && h.Resources.Cores <= 0 && h.Resources.MemoryBytes <= 0 && waitingOwners[h.RunID])
		if !orchestrationWait {
			holders = append(holders, h)
		}
	}
	return holders
}

func resourceAvailable(r wingwire.ResourceState) float64 {
	if isHostResource(r.Key) && (r.Available > 0 || r.Reserved > 0 || r.External > 0 || r.ExternalSource != "") {
		return r.Available
	}
	free := r.Capacity - r.Held
	if free < 0 {
		free = 0
	}
	return free
}

func resourceLegend(qs wingwire.QueueState) string {
	for _, r := range qs.Resources {
		if isHostResource(r.Key) {
			return "available = capacity - in use - reserved (kept free for the rest of the machine) - external (other processes, smoothed)"
		}
	}
	return ""
}

func fmtHeadroomCell(key string, v float64) string {
	if !isHostResource(key) {
		return "-"
	}
	return fmtAmount(key, v)
}

func externalCell(r wingwire.ResourceState) string {
	if !isHostResource(r.Key) {
		return "-"
	}
	if r.ExternalSource == wingwire.ExternalUnmeasured {
		return "unmeasured"
	}
	return fmtAmount(r.Key, r.External)
}

func ExternalUnmeasuredNote(qs wingwire.QueueState) string {
	if qs.IgnoreExternal {
		return ""
	}
	var keys []string
	for _, r := range qs.Resources {
		if isHostResource(r.Key) && r.ExternalSource == wingwire.ExternalUnmeasured {
			keys = append(keys, r.Key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	return "external: unmeasured on " + strings.Join(keys, ", ") +
		" (host sensor unavailable); no external load subtracted from available"
}

func ExternalAgeNote(qs wingwire.QueueState) string {
	if qs.ExternalSampleAgeMS <= 0 {
		return ""
	}
	note := "external reading: " + fmtElapsed(qs.ExternalSampleAgeMS) + " old"
	if qs.ExternalMeasurementAgeMS > 0 && qs.ExternalMeasurementAgeMS < qs.ExternalSampleAgeMS {
		note += " (host sampled " + fmtElapsed(qs.ExternalMeasurementAgeMS) + " ago)"
	}
	return note
}

func isHostResource(key string) bool { return key == "cores" || key == "memory" }

func ContainerNote(c *wingwire.ContainerLimit) string {
	if c == nil {
		return ""
	}
	var parts []string
	if c.Cores > 0 && c.HostCores > 0 {
		parts = append(parts, fmt.Sprintf("%.1f cores (host %.1f)", c.Cores, c.HostCores))
	}
	if c.MemoryBytes > 0 && c.HostMemoryBytes > 0 {
		parts = append(parts, fmt.Sprintf("%s memory (host %s)",
			humanBytes(c.MemoryBytes), humanBytes(c.HostMemoryBytes)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "container limit: " + strings.Join(parts, ", ")
}

func BudgetNote(b *wingwire.BudgetState) string {
	if b == nil {
		return ""
	}
	if b.Source == string(wingwire.BudgetSourceUnset) {
		return "budget: none set (admission plans against the whole machine)"
	}
	var parts []string
	if b.MachineCores > 0 && b.Cores < b.MachineCores {
		parts = append(parts, fmt.Sprintf("%.1f cores (machine %.1f)", b.Cores, b.MachineCores))
	}
	if b.MachineMemoryBytes > 0 && b.MemoryBytes < b.MachineMemoryBytes {
		parts = append(parts, fmt.Sprintf("%s memory (machine %s)",
			humanBytes(b.MemoryBytes), humanBytes(b.MachineMemoryBytes)))
	}
	if len(parts) == 0 && b.Source == "" {
		return ""
	}
	note := "budget"
	if len(parts) > 0 {
		note += " " + strings.Join(parts, ", ")
	} else {
		note += ": no capacity cap"
	}
	if b.Enforce {
		note += "; OS-enforced"
	}
	if src := BudgetSourceLabel(b); src != "" {
		note += " (" + src + ")"
	}
	return note
}

func budgetOrigin(b *wingwire.BudgetState) string {
	if b == nil {
		return ""
	}
	return b.Origin
}

func BudgetSourceLabel(b *wingwire.BudgetState) string {
	if b == nil {
		return ""
	}
	switch wingwire.BudgetSource(b.Source) {
	case wingwire.BudgetSourceConfig:
		return "from config " + b.Origin
	case wingwire.BudgetSourceEnv:
		return "from " + b.Origin + " in the environment of whatever spawned the daemon"
	case wingwire.BudgetSourceFlag:
		return "from the daemon's " + b.Origin + " flag"
	case wingwire.BudgetSourceUnknown:
		return "source unrecorded: set directly by the binary that started the daemon"
	default:
		return ""
	}
}

func ExternalIgnoredNote(qs wingwire.QueueState) string {
	if !qs.IgnoreExternal {
		return ""
	}
	note := "external: ignored (operator setting"
	if src := BudgetSourceLabel(qs.Budget); src != "" {
		note += ", " + src
	}
	return note + ")"
}

func ExternalPressureNote(qs wingwire.QueueState) string {
	if qs.IgnoreExternal || len(qs.Waiters) == 0 {
		return ""
	}
	for _, r := range qs.Resources {
		if isHostResource(r.Key) && r.External > 0 && r.Held < r.Capacity {
			for _, wt := range qs.Waiters {
				if wt.BlockingReason != "" {
					return "note: external (non-sparkwing) load is the binding constraint; free capacity above is reserved or already in use by other processes"
				}
			}
		}
	}
	return ""
}

type queueDriftNote struct {
	runID   string
	warning string
}

func queueDriftNotes(qs wingwire.QueueState) []queueDriftNote {
	var notes []queueDriftNote
	for _, h := range qs.Holders {
		if h.DriftWarning != "" {
			notes = append(notes, queueDriftNote{h.RunID, h.DriftWarning})
		}
	}
	for _, wt := range qs.Waiters {
		if wt.DriftWarning != "" {
			notes = append(notes, queueDriftNote{wt.RunID, wt.DriftWarning})
		}
	}
	return notes
}

func FmtDaemonHeader(qs wingwire.QueueState) string {
	if qs.DaemonVersion == "" && qs.DaemonUptimeMS <= 0 {
		return ""
	}
	version := qs.DaemonVersion
	if version == "" {
		version = "unknown"
	}
	up := "just started"
	if qs.DaemonUptimeMS > 0 {
		up = "up " + (time.Duration(qs.DaemonUptimeMS) * time.Millisecond).Round(time.Second).String()
	}
	return fmt.Sprintf("daemon %s, %s", version, up)
}

func FmtCapacityChange(cc *wingwire.CapacityChange) string {
	if cc == nil || cc.FromCores == cc.ToCores {
		return ""
	}
	return fmt.Sprintf("capacity changed: %s -> %s cores", trimFloat(cc.FromCores), trimFloat(cc.ToCores))
}

func FmtEventsLine(ev *wingwire.EventsWindow) string {
	if ev == nil || (ev.Runs == 0 && len(ev.Evictions) == 0 && ev.QueueTimeouts == 0 &&
		ev.Cancellations == 0 && ev.Contended == 0 && ev.Backfills == 0 && ev.BackfillProtections == 0) {
		return ""
	}
	span := (time.Duration(ev.WindowMS) * time.Millisecond).Round(time.Hour)
	label := span.String()
	if h := int(span.Hours()); h > 0 {
		label = fmt.Sprintf("%dh", h)
	}
	parts := []string{fmt.Sprintf("%d %s", ev.Runs, pluralWord(ev.Runs, "run", "runs"))}
	if ev.Runs > 0 {
		parts = append(parts, fmt.Sprintf("median wait %s",
			(time.Duration(ev.MedianWaitMS)*time.Millisecond).Round(time.Second)))
	}
	if n := totalEvictions(ev.Evictions); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s (%s)", n, pluralWord(n, "eviction", "evictions"),
			evictionKeys(ev.Evictions)))
	}
	if ev.QueueTimeouts > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", ev.QueueTimeouts,
			pluralWord(ev.QueueTimeouts, "queue-timeout", "queue-timeouts")))
	}
	if ev.Cancellations > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", ev.Cancellations,
			pluralWord(ev.Cancellations, "cancellation", "cancellations")))
	}
	if ev.Contended > 0 {
		parts = append(parts, fmt.Sprintf("%d contended", ev.Contended))
	}
	if ev.Backfills > 0 {
		parts = append(parts, fmt.Sprintf("%d younger %s", ev.Backfills,
			pluralWord(ev.Backfills, "backfill", "backfills")))
	}
	if ev.BackfillProtections > 0 {
		parts = append(parts, fmt.Sprintf("%d %s protected", ev.BackfillProtections,
			pluralWord(ev.BackfillProtections, "waiter", "waiters")))
	}
	out := "last " + label + ": "
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func totalEvictions(counts []wingwire.EvictionCount) int {
	n := 0
	for _, c := range counts {
		n += c.Count
	}
	return n
}

func evictionKeys(counts []wingwire.EvictionCount) string {
	if len(counts) == 1 {
		return "key: " + counts[0].Key
	}
	out := "keys: "
	for i, c := range counts {
		if i > 0 {
			out += ", "
		}
		out += c.Key
	}
	return out
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func fmtHolderCost(h wingwire.Holder) string {
	if h.Parent != "" {
		return "-"
	}
	return fmtCost(h.Resources)
}

func fmtETA(ms *int64) string {
	if ms == nil {
		return "-"
	}
	if *ms <= 0 {
		return "now"
	}
	return (time.Duration(*ms) * time.Millisecond).Round(time.Second).String()
}

func fmtAmount(key string, v float64) string {
	if key == "memory" {
		return humanBytes(int64(v))
	}
	return trimFloat(v)
}

func fmtCost(r wingwire.HostResources) string {
	out := trimFloat(r.Cores) + " cores"
	if r.MemoryBytes > 0 {
		out += ", " + humanBytes(r.MemoryBytes)
	}
	return out
}

func fmtElapsed(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

func trimFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.2f", v)
}

func joinKeys(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += k
	}
	return out
}

func OriginWord(o wingwire.Origin) string {
	if o == "" {
		return string(wingwire.OriginLocal)
	}
	return string(o)
}

func stalledWord(h wingwire.Holder) string {
	switch {
	case h.Stalled:
		return "stalled"
	case h.Contended:
		return "contended"
	default:
		return "live"
	}
}

func humanBytes(n int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func queueDisplayRunID(runID, displayRunID string) string {
	if displayRunID != "" {
		return displayRunID
	}
	return runID
}

func queueParentID(h wingwire.Holder) string {
	if h.ParentParticipantID != "" {
		return h.ParentParticipantID
	}
	return h.Parent
}
