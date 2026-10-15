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

	"github.com/sparkwing-dev/sparkwing/internal/crons"
)

// safety: matches TimeoutStartSec in the systemd unit, so a hung tick is cut
// by whichever side notices first instead of holding the minute open.
const cronsTickTimeout = 5 * time.Minute

func runCronsTick(args []string) error {
	fs := flag.NewFlagSet(cmdCronsTick.Path, flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "evaluate and report without launching or recording anything")
	outFmt := cronsOutputFlag(fs)
	nowFlag := cronsNowFlag(fs)
	if err := parseAndCheck(cmdCronsTick, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsTick.Path)
	if err != nil {
		return err
	}
	session, release, err := openCrons(*nowFlag)
	if err != nil {
		return fmt.Errorf("__tick: %w", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), cronsTickTimeout)
	defer cancel()
	session.svc.Schedules = nil
	report, err := session.svc.Tick(ctx, *dryRun)
	switch {
	case errors.Is(err, crons.ErrTickRunning):
		// safety: the tick holding the lock is resolving this minute, so this
		// one has nothing to do and exits clean for the timer.
		fmt.Fprintln(os.Stdout, "tick: another tick is already running")
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("__tick: gave up after %s; the store or a launch is not answering, and the next minute's tick will try again", cronsTickTimeout)
	case err != nil:
		return fmt.Errorf("__tick: %w", err)
	}
	return renderCronsTick(os.Stdout, report, *dryRun, format)
}

func renderCronsTick(w io.Writer, report crons.TickReport, dryRun bool, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(report)
	case "plain":
		_, err := fmt.Fprintln(w, report.Summary())
		return err
	}
	prefix := "tick"
	if dryRun {
		prefix = "tick (dry run)"
	}
	fmt.Fprintf(w, "%s: %s\n", prefix, report.Summary())
	if dryRun && len(report.Decisions) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "DUE\tOUTCOME\tNAME\tDETAIL")
		for _, d := range report.Decisions {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				d.Due.Format(time.RFC3339), d.Outcome, crons.DisplayName(d.Schedule), dashIfEmpty(d.Detail))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if !dryRun {
		for _, d := range report.Decisions {
			if d.RunID != "" {
				fmt.Fprintf(w, "  %s -> %s\n", crons.DisplayName(d.Schedule), d.RunID)
			}
		}
	}
	for _, e := range report.Errors {
		fmt.Fprintf(w, "  error: %s\n", e)
	}
	return nil
}
