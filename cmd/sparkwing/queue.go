// `sparkwing queue` -- the one truthful view of local admission. It
// reads the local daemon's queue state and renders every resource with
// its capacity and in-use amount, every holder with elapsed time and
// cost, and every waiter in arrival order with what it is waiting on. A
// holder that is alive but idle while runs queue behind it is flagged
// with the exact non-destructive recovery command. With no daemon
// running there is nothing to arbitrate, so the command reports an empty
// queue and exits 0.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func runQueue(args []string) error {
	fs := flag.NewFlagSet(cmdQueue.Path, flag.ContinueOnError)
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	home := fs.String("home", "", "sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)")
	if err := parseAndCheck(cmdQueue, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdQueue.Path)
	if err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("queue: unexpected positional %q (queue takes flags only)", fs.Arg(0))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// safety: empty Version keeps this read-only view from ever draining
	// or replacing a running daemon during the version handshake.
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: *home})
	legacy, _ := liveLegacyBoxSlots(*home)

	if err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			if rerr := renderNoDaemon(os.Stdout, format); rerr != nil {
				return rerr
			}
			warnLegacy(os.Stderr, len(legacy))
			return nil
		}
		return fmt.Errorf("queue: %w", err)
	}
	if rerr := renderQueue(os.Stdout, qs, format); rerr != nil {
		return rerr
	}
	warnLegacy(os.Stderr, len(legacy))
	return nil
}

// warnLegacy prints the legacy-coexistence warning to stderr when
// older-pinned pipeline binaries are still admitting outside the daemon.
// It goes to stderr so JSON and plain stdout stay machine-clean.
func warnLegacy(w io.Writer, n int) {
	if line := legacyWarningLine(n); line != "" {
		fmt.Fprintf(w, "warning: %s\n", line)
	}
}

// renderNoDaemon reports the calm truth that nothing is queued: no daemon
// means no admission is being arbitrated. JSON callers still get a
// well-formed empty queue so a pipeline never special-cases the string.
func renderNoDaemon(w io.Writer, format string) error {
	switch format {
	case "json":
		return renderQueue(w, wingwire.QueueState{}, format)
	case "plain":
		return nil
	default:
		fmt.Fprintln(w, "no admission daemon running; nothing is queued")
		return nil
	}
}

func renderQueue(w io.Writer, qs wingwire.QueueState, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(qs)
	case "plain":
		return renderQueuePlain(w, qs)
	default:
		return renderQueuePretty(w, qs)
	}
}

// renderQueuePlain emits one tab-separated record per line, tagged by
// kind so a shell pipeline can filter with grep/awk.
func renderQueuePlain(w io.Writer, qs wingwire.QueueState) error {
	for _, r := range qs.Resources {
		fmt.Fprintf(w, "resource\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Key,
			fmtAmount(r.Key, r.Capacity), fmtAmount(r.Key, r.Held),
			fmtHeadroomCell(r.Key, r.Reserved), fmtHeadroomCell(r.Key, r.External),
			fmtAmount(r.Key, resourceAvailable(r)))
	}
	for _, h := range qs.Holders {
		fmt.Fprintf(w, "holder\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h.RunID, orDash(h.Pipeline), orDash(h.Repo), fmtElapsed(h.ElapsedMS), fmtHolderCost(h),
			orDash(h.CostSource), joinKeys(h.Semaphores), stalledWord(h), orDash(h.Parent))
	}
	for _, wt := range qs.Waiters {
		fmt.Fprintf(w, "waiter\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			wt.Position, wt.RunID, orDash(wt.Pipeline), orDash(wt.Repo), fmtCost(wt.Resources),
			orDash(wt.CostSource), fmtETA(wt.ExpectedStartMS),
			joinKeys(wt.WaitingOn), fmtElapsed(wt.WaitingMS), orDash(wt.BlockingReason))
	}
	return nil
}

func renderQueuePretty(out io.Writer, qs wingwire.QueueState) error {
	clear := ""
	if qs.ExpectedClearMS != nil && *qs.ExpectedClearMS > 0 {
		clear = fmt.Sprintf("; clears in ~%s", fmtElapsed(*qs.ExpectedClearMS))
	}
	if d := fmtDaemonHeader(qs); d != "" {
		fmt.Fprintln(out, d)
	}
	fmt.Fprintf(out, "local admission: %d holding, %d queued%s\n", len(qs.Holders), len(qs.Waiters), clear)
	if line := fmtEventsLine(qs.Events); line != "" {
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
			fmtHeadroomCell(r.Key, r.Reserved), fmtHeadroomCell(r.Key, r.External),
			fmtAmount(r.Key, resourceAvailable(r)))
	}
	_ = tw.Flush()

	if note := externalPressureNote(qs); note != "" {
		fmt.Fprintf(out, "\n%s\n", note)
	}

	fmt.Fprintln(out)
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tPIPELINE\tREPO\tELAPSED\tCOST\tSOURCE\tSEMAPHORES")
	if len(qs.Holders) == 0 {
		fmt.Fprintln(tw, "(none holding)\t\t\t\t\t\t")
	}
	for _, h := range qs.Holders {
		run := h.RunID
		if h.Parent != "" {
			run = "  " + run + " (attached)"
		}
		if h.Stalled {
			run += " (stalled)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", run, orDash(h.Pipeline), orDash(h.Repo),
			fmtElapsed(h.ElapsedMS), fmtHolderCost(h), orDash(h.CostSource), orDash(joinKeys(h.Semaphores)))
	}
	_ = tw.Flush()

	fmt.Fprintln(out)
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "POS\tRUN\tPIPELINE\tREPO\tCOST\tSOURCE\tETA\tWAITING ON\tWAITED")
	if len(qs.Waiters) == 0 {
		fmt.Fprintln(tw, "-\t(no one queued)\t\t\t\t\t\t\t")
	}
	for _, wt := range qs.Waiters {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", wt.Position, wt.RunID, orDash(wt.Pipeline),
			orDash(wt.Repo), fmtCost(wt.Resources), orDash(wt.CostSource), fmtETA(wt.ExpectedStartMS),
			orDash(joinKeys(wt.WaitingOn)), fmtElapsed(wt.WaitingMS))
	}
	_ = tw.Flush()

	for _, wt := range qs.Waiters {
		if wt.BlockingReason != "" {
			fmt.Fprintf(out, "\n%s waiting: %s\n", wt.RunID, wt.BlockingReason)
		}
	}
	for _, h := range qs.Holders {
		if h.Stalled && h.Recovery != "" {
			fmt.Fprintf(out, "\n%s is stalled (idle while runs wait). Recover with:\n  %s\n", h.RunID, h.Recovery)
		}
	}
	for _, d := range queueDriftNotes(qs) {
		fmt.Fprintf(out, "\n%s: %s\n", d.runID, d.warning)
	}
	return nil
}

// resourceAvailable is the grantable amount to show for a resource row:
// the daemon's headroom-aware Available for the host dimensions, or plain
// capacity-minus-held for a semaphore row (and for older daemons that
// sent no Available).
func resourceAvailable(r wingwire.ResourceState) float64 {
	if isHostResource(r.Key) && (r.Available > 0 || r.Reserved > 0 || r.External > 0) {
		return r.Available
	}
	free := r.Capacity - r.Held
	if free < 0 {
		free = 0
	}
	return free
}

// fmtHeadroomCell renders a reserve/external cell: a dash for semaphore
// rows, which have no headroom decomposition.
func fmtHeadroomCell(key string, v float64) string {
	if !isHostResource(key) {
		return "-"
	}
	return fmtAmount(key, v)
}

func isHostResource(key string) bool { return key == "cores" || key == "memory" }

// externalPressureNote returns a one-line callout when non-sparkwing load
// is what is holding runs back -- a queue that looks idle (free capacity,
// no holders) but refuses work because the machine is busy with other
// processes. Empty when external load is not the binding constraint.
func externalPressureNote(qs wingwire.QueueState) string {
	if len(qs.Waiters) == 0 {
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

// queueDriftNote pairs a run with its pin-drift warning for the bottom-of-
// view callout.
type queueDriftNote struct {
	runID   string
	warning string
}

// queueDriftNotes collects the pin-drift warnings across holders and
// waiters so the pretty view surfaces the exact fix once per run.
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

// fmtDaemonHeader renders the daemon identity line above the queue: its
// binary version and how long it has been up. Empty when the daemon
// reported neither (an older daemon, or no daemon at all).
func fmtDaemonHeader(qs wingwire.QueueState) string {
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

// fmtEventsLine renders the one-line recent-events health summary from
// the daemon's rolling window: run count with median wait, then only the
// trouble categories that actually occurred. Empty when the daemon sent
// no window or nothing happened in it.
func fmtEventsLine(ev *wingwire.EventsWindow) string {
	if ev == nil || (ev.Runs == 0 && len(ev.Evictions) == 0 && ev.QueueTimeouts == 0 && ev.Cancellations == 0) {
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

// evictionKeys names the contested keys behind an eviction tally:
// "key: land" for one key, "keys: deploy, land" for several.
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

// fmtHolderCost renders a holder's charge, or a dash for an attached
// child, which rides its parent's lease and is charged nothing.
func fmtHolderCost(h wingwire.Holder) string {
	if h.Parent != "" {
		return "-"
	}
	return fmtCost(h.Resources)
}

// fmtETA renders a waiter's estimated start offset: "now" when it is
// admitted immediately, a rounded duration when it must wait, or "-" when
// no estimate is available.
func fmtETA(ms *int64) string {
	if ms == nil {
		return "-"
	}
	if *ms <= 0 {
		return "now"
	}
	return (time.Duration(*ms) * time.Millisecond).Round(time.Second).String()
}

// fmtAmount renders a resource amount: memory keys as human bytes, every
// other dimension (cores, semaphore costs) as a plain number.
func fmtAmount(key string, v float64) string {
	if key == "memory" {
		return humanBytes(int64(v))
	}
	return trimFloat(v)
}

// fmtCost renders a holder's or waiter's host charge as "<cores> cores"
// plus memory when charged.
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

// trimFloat prints a float without trailing zero noise: whole numbers
// render bare, fractions to two places.
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

func stalledWord(h wingwire.Holder) string {
	if h.Stalled {
		return "stalled"
	}
	return "live"
}
