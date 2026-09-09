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

func runCronsPause(args []string) error {
	return runCronsPauseResume(cmdCronsPause, args, true)
}

func runCronsResume(args []string) error {
	return runCronsPauseResume(cmdCronsResume, args, false)
}

func runCronsPauseResume(cmd Command, args []string, pause bool) error {
	verb := "resume"
	if pause {
		verb = "pause"
	}
	fs := flag.NewFlagSet(cmd.Path, flag.ContinueOnError)
	outFmt := cronsOutputFlag(fs)
	if err := parseAndCheck(cmd, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		PrintHelp(cmd, os.Stderr)
		return fmt.Errorf("crons %s: one schedule name is required", verb)
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmd.Path)
	if err != nil {
		return err
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons %s: %w", verb, err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if pause {
		err = session.svc.Pause(ctx, sched.ID)
	} else {
		err = session.svc.Resume(ctx, sched.ID)
	}
	if err != nil {
		return fmt.Errorf("crons %s: %w", verb, err)
	}
	row, _, err := session.svc.Show(ctx, sched.ID, 1)
	if err != nil {
		return fmt.Errorf("crons %s: %w", verb, err)
	}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(row)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, row.ID)
		return perr
	}
	fmt.Fprintf(os.Stdout, "%s is %s\n", row.Name, row.State)
	if !pause && row.NextDueAt != nil {
		fmt.Fprintf(os.Stdout, "  next: %s\n", cronAbsTime(*row.NextDueAt, row.Location))
	}
	return nil
}

// cronsRunReport is what `crons run` launched.
type cronsRunReport struct {
	Schedule string `json:"schedule"`
	Name     string `json:"name"`
	Pipeline string `json:"pipeline"`
	RunID    string `json:"run_id"`
}

func runCronsRun(args []string) error {
	fs := flag.NewFlagSet(cmdCronsRun.Path, flag.ContinueOnError)
	outFmt := cronsOutputFlag(fs)
	if err := parseAndCheck(cmdCronsRun, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		PrintHelp(cmdCronsRun, os.Stderr)
		return errors.New("crons run: one schedule name is required")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsRun.Path)
	if err != nil {
		return err
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons run: %w", err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	runID, err := session.svc.RunNow(ctx, sched.ID)
	if err != nil {
		return fmt.Errorf("crons run: %w", err)
	}
	report := cronsRunReport{
		Schedule: sched.ID, Name: crons.DisplayName(sched), Pipeline: sched.Pipeline, RunID: runID,
	}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(report)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, report.RunID)
		return perr
	}
	fmt.Fprintf(os.Stdout, "run %s submitted (%s)\n", report.RunID, report.Pipeline)
	fmt.Fprintf(os.Stdout, "  follow: sparkwing runs logs --run %s --follow\n", report.RunID)
	fmt.Fprintf(os.Stdout, "  cancel: sparkwing runs cancel --run %s\n", report.RunID)
	return nil
}

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
		return fmt.Errorf("crons tick: %w", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), cronsTickTimeout)
	defer cancel()
	report, err := session.svc.Tick(ctx, *dryRun)
	switch {
	case errors.Is(err, crons.ErrTickRunning):
		// safety: the tick holding the lock is resolving this minute, so this
		// one has nothing to do and exits clean for the timer.
		fmt.Fprintln(os.Stdout, "tick: another tick is already running")
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("crons tick: gave up after %s; the store or a launch is not answering, and the next minute's tick will try again", cronsTickTimeout)
	case err != nil:
		return fmt.Errorf("crons tick: %w", err)
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
