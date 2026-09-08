package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	flag "github.com/spf13/pflag"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const queuePriorityTimeout = 10 * time.Second

// queuePriorityInvisible is the caveat every not-found answer carries: local
// admission only knows runs a consumer has already claimed and started.
const queuePriorityInvisible = "a submitted run the consumer has not claimed yet is not queued here"

type queuePriorityResult struct {
	RunID string `json:"run_id"`
	wingwire.SetPriorityAck
	PreviousPosition int `json:"previous_position,omitempty"`
}

var queuePriorityClientOptions = func(home string) wingdclient.Options {
	opts := wingdclient.Options{Home: home, Version: Version}
	opts.Spawn = func(string, string) error { return wingdclient.ErrNoDaemon }
	opts.NoTakeover = true
	return opts
}

func runQueuePriority(args []string) error {
	fs := flag.NewFlagSet(cmdQueuePriority.Path, flag.ContinueOnError)
	run := fs.String("run", "", "run id to re-rank")
	set := fs.String("set", "", "new priority: an integer, front, or back")
	home := fs.String("home", "", "sparkwing home to inspect (default: $SPARKWING_HOME or ~/.sparkwing)")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdQueuePriority, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("queue priority: unexpected positional %q (queue priority takes flags only)", fs.Arg(0))
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdQueuePriority.Path)
	if err != nil {
		return err
	}
	if *run == "" {
		return fmt.Errorf("queue priority: --run is required")
	}
	priority, mode, err := parseQueuePriority(*set)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), queuePriorityTimeout)
	defer cancel()

	res, err := applyQueuePriority(ctx, *home, *run, priority, mode)
	if err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			// No daemon means nothing is queued, so the run is as absent as it
			// gets rather than a machine this command failed to reach.
			return renderQueuePriorityMissing(os.Stdout, format, *run, "no admission daemon is running")
		}
		if errors.Is(err, wingdclient.ErrDaemonUnreachable) {
			return exitError(4, fmt.Errorf("queue priority: %w", err))
		}
		return fmt.Errorf("queue priority: %w", err)
	}
	if !res.Found {
		return renderQueuePriorityMissing(os.Stdout, format, *run, res.Reason)
	}
	return renderQueuePriority(os.Stdout, res, format)
}

func parseQueuePriority(set string) (priority int, mode string, err error) {
	switch set {
	case "":
		return 0, "", fmt.Errorf("queue priority: --set is required (an integer, front, or back)")
	case "front", "back":
		return 0, set, nil
	}
	n, perr := strconv.Atoi(set)
	if perr != nil {
		return 0, "", fmt.Errorf("queue priority: --set %q is not an integer, front, or back", set)
	}
	return n, "", nil
}

func applyQueuePriority(ctx context.Context, home, runID string, priority int, mode string) (queuePriorityResult, error) {
	cl, err := wingdclient.EnsureDaemon(ctx, queuePriorityClientOptions(home))
	if err != nil {
		return queuePriorityResult{}, err
	}
	defer func() { _ = cl.Close() }()

	res := queuePriorityResult{RunID: runID}
	// The ack carries the rank's landing place; the position it left comes from
	// the queue as it stands before the change.
	if qs, qerr := cl.QueueState(ctx); qerr == nil {
		for _, w := range qs.Waiters {
			if w.RunID == runID {
				res.PreviousPosition = w.Position
				break
			}
		}
	}
	ack, err := cl.SetPriority(ctx, runID, priority, mode)
	if err != nil {
		return queuePriorityResult{}, err
	}
	res.SetPriorityAck = ack
	return res, nil
}

func renderQueuePriorityMissing(out io.Writer, format, runID, reason string) error {
	if reason == "" {
		reason = "not in local admission"
	}
	res := queuePriorityResult{RunID: runID, SetPriorityAck: wingwire.SetPriorityAck{Reason: reason}}
	switch format {
	case "json":
		if err := queuePriorityJSON(out, res); err != nil {
			return err
		}
	case "plain":
		fmt.Fprintf(out, "priority\t%s\tnot_found\t%s\n", runID, reason)
	default:
		fmt.Fprintf(out, "run %s: %s\n", runID, reason)
	}
	return exitError(1, fmt.Errorf("run %s: %s; %s", runID, reason, queuePriorityInvisible))
}

func renderQueuePriority(out io.Writer, res queuePriorityResult, format string) error {
	switch format {
	case "json":
		return queuePriorityJSON(out, res)
	case "plain":
		fmt.Fprintf(out, "priority\t%s\t%d\t%d\t%d\t%d\t%d\t%t\n",
			res.RunID, res.Previous, res.Priority, res.PreviousPosition, res.Position, res.Participants, res.Holding)
		return nil
	default:
		line := fmt.Sprintf("run %s: priority %d -> %d", res.RunID, res.Previous, res.Priority)
		if res.PreviousPosition > 0 || res.Position > 0 {
			line += fmt.Sprintf(", position %s -> %s",
				queuePositionWord(res.PreviousPosition), queuePositionWord(res.Position))
		}
		fmt.Fprintln(out, line)
		if res.Holding {
			fmt.Fprintf(out, "run %s already holds a lease, which keeps its place; its waiting participants moved, and the node admissions it has yet to make land at %d\n", res.RunID, res.Priority)
		}
		return nil
	}
}

func queuePositionWord(n int) string {
	if n <= 0 {
		return "not queued"
	}
	return strconv.Itoa(n)
}

func queuePriorityJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
