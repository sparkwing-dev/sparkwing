package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

const queuePriorityTimeout = 10 * time.Second

type queuePriorityResult = controllerwire.QueuePriorityResult

func queuePriorityInput(inv clisurface.Invocation) (controllerwire.QueuePriorityWrite, error) {
	var input controllerwire.QueuePriorityWrite
	if inv.Command.Path != "sparkwing queue priority" {
		return input, nil
	}
	priority, mode, err := parseQueuePriority(inv.Value("set"))
	if err != nil {
		return input, &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
	}
	input = controllerwire.QueuePriorityWrite{RunID: inv.Value("run"), Mode: mode}
	if mode == "" {
		input.Priority = &priority
	}
	if err := input.Validate(); err != nil {
		return input, &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
	}
	if inv.Value("group") == "" && inv.Value("profile") != "" {
		return input, &apierr.Error{Code: apierr.PolicyMissing, Message: "remote run priority has no authorized controller route", Hint: "select a concurrency group with --group"}
	}
	if inv.Value("group") != "" {
		input.IdempotencyKey = rand.Text()
	}
	return input, nil
}

func parseQueuePriority(set string) (priority int, mode string, err error) {
	switch set {
	case "":
		return 0, "", fmt.Errorf("--set is required (an integer, front, or back)")
	case "front", "back":
		return 0, set, nil
	}
	n, perr := strconv.Atoi(set)
	if perr != nil {
		return 0, "", fmt.Errorf("--set %q is not an integer, front, or back", set)
	}
	return n, "", nil
}

func applyQueuePriority(ctx context.Context, home, runID string, priority int, mode string) (queuePriorityResult, error) {
	opts := wingdclient.Options{Home: home, Version: installedVersion(), NoTakeover: true}
	opts.Spawn = func(string, string) error { return wingdclient.ErrNoDaemon }
	cl, err := wingdclient.EnsureDaemon(ctx, opts)
	if errors.Is(err, wingdclient.ErrNoDaemon) {
		return queuePriorityResult{RunID: runID}, nil
	}
	if err != nil {
		return queuePriorityResult{}, err
	}
	defer func() {
		if err := cl.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "queue priority: closing the daemon connection: %v\n", err)
		}
	}()

	res := queuePriorityResult{RunID: runID}
	qs, err := cl.QueueState(ctx)
	if err != nil {
		return queuePriorityResult{}, err
	}
	for _, w := range qs.Waiters {
		if w.RunID == runID {
			res.PreviousPosition = w.Position
			break
		}
	}
	ack, err := cl.SetPriority(ctx, runID, priority, mode)
	if err != nil {
		return queuePriorityResult{}, err
	}
	res.SetPriorityAck = ack
	return res, nil
}

func renderQueuePriorityMissing(out io.Writer, format string, res queuePriorityResult) error {
	runID, reason := res.RunID, res.Reason
	if reason == "" {
		reason = "not in local admission"
		if res.Group != "" {
			reason = "not in concurrency group " + res.Group
		}
	}
	res.Reason = reason
	switch format {
	case "json":
		if err := queuePriorityJSON(out, res); err != nil {
			return err
		}
	case "plain":
		if _, err := fmt.Fprintf(out, "priority\t%s\tnot_found\t%s\n", runID, reason); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintf(out, "run %s: %s\n", runID, reason); err != nil {
			return err
		}
	}
	return exitError(1, fmt.Errorf("run %s: %s", runID, reason))
}

func renderQueuePriority(out io.Writer, res queuePriorityResult, format string) error {
	switch format {
	case "json":
		return queuePriorityJSON(out, res)
	case "plain":
		_, err := fmt.Fprintf(out, "priority\t%s\t%d\t%d\t%d\t%d\t%d\t%t\n",
			res.RunID, res.Previous, res.Priority, res.PreviousPosition, res.Position, res.Participants, res.Holding)
		return err
	default:
		line := fmt.Sprintf("run %s: priority %d -> %d", res.RunID, res.Previous, res.Priority)
		if res.PreviousPosition > 0 || res.Position > 0 {
			line += fmt.Sprintf(", position %s -> %s",
				queuePositionWord(res.PreviousPosition), queuePositionWord(res.Position))
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
		if res.Holding {
			_, err := fmt.Fprintf(out, "run %s already holds a lease, which keeps its place; its waiting participants moved, and the job admissions it has yet to make land at %d\n", res.RunID, res.Priority)
			return err
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
	return enc.Encode(v)
}
