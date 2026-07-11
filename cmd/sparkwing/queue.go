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
	if err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			return renderNoDaemon(os.Stdout, format)
		}
		return fmt.Errorf("queue: %w", err)
	}
	return renderQueue(os.Stdout, qs, format)
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
		fmt.Fprintf(w, "resource\t%s\t%s\t%s\n", r.Key, fmtAmount(r.Key, r.Capacity), fmtAmount(r.Key, r.Held))
	}
	for _, h := range qs.Holders {
		fmt.Fprintf(w, "holder\t%s\t%s\t%s\t%s\t%s\t%s\n",
			h.RunID, orDash(h.Pipeline), fmtElapsed(h.ElapsedMS), fmtCost(h.Resources),
			joinKeys(h.Semaphores), stalledWord(h))
	}
	for _, wt := range qs.Waiters {
		fmt.Fprintf(w, "waiter\t%d\t%s\t%s\t%s\t%s\t%s\n",
			wt.Position, wt.RunID, orDash(wt.Pipeline), fmtCost(wt.Resources),
			joinKeys(wt.WaitingOn), fmtElapsed(wt.WaitingMS))
	}
	return nil
}

func renderQueuePretty(out io.Writer, qs wingwire.QueueState) error {
	fmt.Fprintf(out, "local admission: %d holding, %d queued\n\n", len(qs.Holders), len(qs.Waiters))

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RESOURCE\tCAPACITY\tIN USE\tFREE")
	if len(qs.Resources) == 0 {
		fmt.Fprintln(tw, "(none)\t\t\t")
	}
	for _, r := range qs.Resources {
		free := r.Capacity - r.Held
		if free < 0 {
			free = 0
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Key,
			fmtAmount(r.Key, r.Capacity), fmtAmount(r.Key, r.Held), fmtAmount(r.Key, free))
	}
	_ = tw.Flush()

	fmt.Fprintln(out)
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tPIPELINE\tELAPSED\tCOST\tSEMAPHORES")
	if len(qs.Holders) == 0 {
		fmt.Fprintln(tw, "(none holding)\t\t\t\t")
	}
	for _, h := range qs.Holders {
		run := h.RunID
		if h.Stalled {
			run += " (stalled)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", run, orDash(h.Pipeline),
			fmtElapsed(h.ElapsedMS), fmtCost(h.Resources), orDash(joinKeys(h.Semaphores)))
	}
	_ = tw.Flush()

	fmt.Fprintln(out)
	tw = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "POS\tRUN\tPIPELINE\tCOST\tWAITING ON\tWAITED")
	if len(qs.Waiters) == 0 {
		fmt.Fprintln(tw, "-\t(no one queued)\t\t\t\t")
	}
	for _, wt := range qs.Waiters {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", wt.Position, wt.RunID, orDash(wt.Pipeline),
			fmtCost(wt.Resources), orDash(joinKeys(wt.WaitingOn)), fmtElapsed(wt.WaitingMS))
	}
	_ = tw.Flush()

	for _, h := range qs.Holders {
		if h.Stalled && h.Recovery != "" {
			fmt.Fprintf(out, "\n%s is stalled (idle while runs wait). Recover with:\n  %s\n", h.RunID, h.Recovery)
		}
	}
	return nil
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
