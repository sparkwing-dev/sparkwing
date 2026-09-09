package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runCronsStatus(args []string) error {
	fs := flag.NewFlagSet(cmdCronsStatus.Path, flag.ContinueOnError)
	outFmt := cronsOutputFlag(fs)
	on := addCronsProfileFlag(fs)
	if err := parseAndCheck(cmdCronsStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsStatus.Path)
	if err != nil {
		return err
	}
	if *on != "" {
		return runCronsStatusProfile(*on, format)
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons status: %w", err)
	}
	defer release()

	host, err := cronsTimerHost(session.paths)
	if err != nil {
		return fmt.Errorf("crons status: %w", err)
	}
	health, err := session.svc.Health(context.Background(), host)
	if err != nil {
		return fmt.Errorf("crons status: %w", err)
	}
	if err := renderCronsHealth(os.Stdout, health, session.now(), format); err != nil {
		return err
	}
	if !health.Healthy() {
		return exitErrorf(1, "crons status: %s", health.Detail)
	}
	return nil
}

func renderCronsHealth(w io.Writer, h crons.Health, now time.Time, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(h)
	case "plain":
		fmt.Fprintf(w, "timer\t%s\n", cronsTimerWord(h))
		fmt.Fprintf(w, "tick\t%s\n", cronsTickWord(h, now))
		fmt.Fprintf(w, "schedules\t%d\t%d\t%d\n", h.Armed, h.Paused, h.Undeclared)
		fmt.Fprintf(w, "locks\t%d\t%d\t%d\t%d\t%d\n",
			h.Locked, h.Following, h.Ahead, h.MissingBinary, h.StaleOverride)
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "timer\t%s\n", cronsTimerWord(h))
	if h.Timer.Path != "" {
		fmt.Fprintf(tw, "unit\t%s\n", h.Timer.Path)
	}
	if h.Timer.Binary != "" {
		fmt.Fprintf(tw, "binary\t%s\n", h.Timer.Binary)
	}
	fmt.Fprintf(tw, "last tick\t%s\n", cronsTickWord(h, now))
	if h.LastTick.Host != "" {
		fmt.Fprintf(tw, "ticked by\t%s, sparkwing %s\n", h.LastTick.Host, dashIfEmpty(h.LastTick.Version))
	}
	if h.LastTick.Error != "" {
		fmt.Fprintf(tw, "tick error\t%s\n", h.LastTick.Error)
	}
	fmt.Fprintf(tw, "schedules\t%d armed, %d paused, %d undeclared\n", h.Armed, h.Paused, h.Undeclared)
	fmt.Fprintf(tw, "locks\t%d locked (%d behind the checkout, %d with the binary gone), %d following\n",
		h.Locked, h.Ahead, h.MissingBinary, h.Following)
	if h.StaleOverride > 0 {
		fmt.Fprintf(tw, "overrides\t%d set against a declaration that has moved\n", h.StaleOverride)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%s\n", h.Detail)
	if h.Remedy != "" {
		fmt.Fprintf(w, "%s\n", h.Remedy)
	}
	return nil
}

func cronsTimerWord(h crons.Health) string {
	switch {
	case h.Timer.Foreign:
		return "foreign (a file sparkwing did not write sits at the unit path)"
	case !h.Timer.Installed:
		return "not installed"
	case h.Timer.Stale:
		return "installed, but it runs another sparkwing"
	case h.Timer.Enabled:
		return "enabled"
	default:
		return "installed, not running"
	}
}

func cronsTickWord(h crons.Health, now time.Time) string {
	if h.LastTick.At.IsZero() {
		return "never"
	}
	word := fmt.Sprintf("%s (%s)", cronRelTime(now, h.LastTick.At), cronAbsTime(h.LastTick.At, time.Local))
	if h.TickStale {
		word += " -- stale"
	}
	return word
}

func runCronsList(args []string) error {
	fs := flag.NewFlagSet(cmdCronsList.Path, flag.ContinueOnError)
	all := fs.Bool("all", false, "include schedules the repo no longer declares")
	outFmt := cronsOutputFlag(fs)
	on := addCronsProfileFlag(fs)
	nowFlag := cronsNowFlag(fs)
	if err := parseAndCheck(cmdCronsList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsList.Path)
	if err != nil {
		return err
	}
	if *on != "" {
		return runCronsListProfile(*on, format, *all)
	}
	session, release, err := openCrons(*nowFlag)
	if err != nil {
		return fmt.Errorf("crons list: %w", err)
	}
	defer release()

	rows, err := session.svc.List(context.Background())
	if err != nil {
		return fmt.Errorf("crons list: %w", err)
	}
	shown, hidden := rows, 0
	if !*all {
		shown = shown[:0:0]
		for _, r := range rows {
			if r.State == crons.StateUndeclared {
				hidden++
				continue
			}
			shown = append(shown, r)
		}
	}
	return renderCronsList(os.Stdout, shown, hidden, session.now(), format)
}

func renderCronsList(w io.Writer, rows []crons.Row, hidden int, now time.Time, format string) error {
	switch format {
	case "json":
		return ndjson.Write(w, rows)
	case "plain":
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, r.Display, r.Effective.Cron, cronsZoneLabel(r.Effective.TZ), cronsLockWord(r), r.State)
		}
		return nil
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no schedules are armed here")
		fmt.Fprintln(w, "run: sparkwing crons install")
		if hidden > 0 {
			fmt.Fprintf(w, "%d schedule(s) the repo no longer declares are hidden; `sparkwing crons list --all` shows them\n", hidden)
		}
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SCHEDULE\tNAME\tCRON\tTZ\tLOCK\tNEXT\tLAST\tOUTCOME\tSTATE")
	overridden, staleOverride := false, false
	for _, r := range rows {
		next := "-"
		if r.NextDueAt != nil {
			next = cronRelTime(now, *r.NextDueAt)
		}
		last := "-"
		if r.LastFiredAt != nil {
			last = cronRelTime(now, *r.LastFiredAt)
		}
		cron := r.Effective.Cron
		if len(r.OverrideFields) > 0 {
			cron += "*"
			overridden = true
			if r.OverrideStale {
				cron += "!"
				staleOverride = true
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Display, cron, cronsZoneLabel(r.Effective.TZ), cronsLockWord(r), next, last,
			dashIfEmpty(r.LastOutcome), r.State)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if overridden {
		fmt.Fprintln(w, "\n* this host overrides the declared value; `sparkwing crons show <name>` has both")
	}
	if staleOverride {
		fmt.Fprintln(w, "! the repo has changed the declaration since that override was set; "+
			"`sparkwing crons install` re-bases it")
	}
	if hidden > 0 {
		fmt.Fprintf(w, "\n%d schedule(s) the repo no longer declares are hidden; `sparkwing crons list --all` shows them\n", hidden)
	}
	return nil
}

type cronsShowReport struct {
	crons.Row
	Fires []cronsFireView `json:"fires,omitempty"`
}

type cronsFireView struct {
	store.CronFire
	RunStatus string `json:"run_status,omitempty"`
}

func runCronsShow(args []string) error {
	fs := flag.NewFlagSet(cmdCronsShow.Path, flag.ContinueOnError)
	fires := fs.Int("fires", 10, "how many recent fires to show")
	outFmt := cronsOutputFlag(fs)
	on := addCronsProfileFlag(fs)
	if err := parseAndCheck(cmdCronsShow, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		PrintHelp(cmdCronsShow, os.Stderr)
		return errors.New("crons show: one schedule name is required")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsShow.Path)
	if err != nil {
		return err
	}
	if *on != "" {
		return runCronsShowProfile(*on, fs.Arg(0), format, *fires)
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons show: %w", err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	row, history, err := session.svc.Show(ctx, sched.ID, *fires)
	if err != nil {
		return fmt.Errorf("crons show: %w", err)
	}
	report := cronsShowReport{Row: row}
	for _, f := range history {
		view := cronsFireView{CronFire: f}
		if f.RunID != "" {
			if run, rerr := session.store.GetRun(ctx, f.RunID); rerr == nil && run != nil {
				view.RunStatus = run.Status
			}
		}
		report.Fires = append(report.Fires, view)
	}
	return renderCronsShow(os.Stdout, report, format)
}

func renderCronsShow(w io.Writer, report cronsShowReport, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(report)
	case "plain":
		_, err := fmt.Fprintln(w, report.Row.ID)
		return err
	}
	r := report.Row
	decl := r.Declaration()
	eff := r.Effective
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "schedule\t%s\n", r.ID)
	fmt.Fprintf(tw, "name\t%s\n", r.Display)
	fmt.Fprintf(tw, "entry\t%s\n", r.ScheduleName)
	fmt.Fprintf(tw, "repo\t%s\n", r.RepoPath)
	fmt.Fprintf(tw, "pipeline\t%s\n", r.Pipeline)
	fmt.Fprintf(tw, "where\t%s\n", dashIfEmpty(r.Where))
	fmt.Fprintln(tw, "\t")
	fmt.Fprintln(tw, "FIELD\tDECLARED\tOVERRIDE\tEFFECTIVE")
	fmt.Fprintf(tw, "cron\t%s\t%s\t%s\n", decl.Cron, cronsOverrideValue(r.Override, "cron"), eff.Cron)
	fmt.Fprintf(tw, "tz\t%s\t%s\t%s\n",
		cronsZoneLabel(decl.TZ), cronsOverrideValue(r.Override, "tz"), cronsZoneLabel(eff.TZ))
	fmt.Fprintf(tw, "overlap\t%s\t%s\t%s\n", decl.Overlap, cronsOverrideValue(r.Override, "overlap"), eff.Overlap)
	fmt.Fprintf(tw, "catch up\t%s\t%s\t%s\n", decl.CatchUp, cronsOverrideValue(r.Override, "catch_up"), eff.CatchUp)
	fmt.Fprintf(tw, "args\t%s\t%s\t%s\n",
		cronsArgsLabel(decl.Args), cronsOverrideValue(r.Override, "args"), cronsArgsLabel(eff.Args))
	fmt.Fprintln(tw, "\t")
	fmt.Fprintf(tw, "lock\t%s\n", cronsShowLock(r))
	if r.Lock.Binary != "" {
		fmt.Fprintf(tw, "binary\t%s\n", r.Lock.Binary)
		fmt.Fprintf(tw, "digest\t%s\n", dashIfEmpty(r.Lock.Digest))
	}
	if r.Override != nil {
		stale := "no"
		if r.OverrideStale {
			stale = "yes: the repo has changed the declaration since; `sparkwing crons install` re-bases it"
		}
		fmt.Fprintf(tw, "override set\t%s\n", cronAbsTime(r.Override.SetAt, r.Location))
		fmt.Fprintf(tw, "override stale\t%s\n", stale)
	}
	fmt.Fprintf(tw, "state\t%s\n", r.State)
	fmt.Fprintf(tw, "armed\t%s by %s\n", cronAbsTime(r.ArmedAt, r.Location), dashIfEmpty(r.ArmedBy))
	fmt.Fprintf(tw, "cursor\t%s\n", cronAbsTime(r.CursorAt, r.Location))
	next := "-"
	if r.NextDueAt != nil {
		next = cronAbsTime(*r.NextDueAt, r.Location)
	}
	fmt.Fprintf(tw, "next due\t%s\n", next)
	lastFired := "-"
	if r.LastFiredAt != nil {
		lastFired = cronAbsTime(*r.LastFiredAt, r.Location)
	}
	fmt.Fprintf(tw, "last fired\t%s\n", lastFired)
	fmt.Fprintf(tw, "last run\t%s\n", dashIfEmpty(r.LastRunID))
	fmt.Fprintf(tw, "last outcome\t%s\n", dashIfEmpty(r.LastOutcome))
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(report.Fires) == 0 {
		fmt.Fprintln(w, "\nno instants have resolved yet")
		return nil
	}
	fmt.Fprintln(w)
	ft := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(ft, "DUE\tDECIDED\tOUTCOME\tRUN\tARGS\tDETAIL")
	for _, f := range report.Fires {
		run := dashIfEmpty(f.RunID)
		if f.RunStatus != "" {
			run = fmt.Sprintf("%s (%s)", f.RunID, f.RunStatus)
		}
		fmt.Fprintf(ft, "%s\t%s\t%s\t%s\t%s\t%s\n",
			cronAbsTime(f.DueAt, r.Location), cronAbsTime(f.DecidedAt, r.Location),
			f.Outcome, run, cronsArgsLabel(f.Args), dashIfEmpty(f.Detail))
	}
	return ft.Flush()
}

type cronsUpcoming struct {
	Schedule string    `json:"schedule"`
	Name     string    `json:"name"`
	At       time.Time `json:"at"`
}

func runCronsNext(args []string) error {
	fs := flag.NewFlagSet(cmdCronsNext.Path, flag.ContinueOnError)
	count := fs.Int("count", 5, "how many instants to show")
	outFmt := cronsOutputFlag(fs)
	on := addCronsProfileFlag(fs)
	nowFlag := cronsNowFlag(fs)
	if err := parseAndCheck(cmdCronsNext, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("crons next: unexpected positional %q", fs.Arg(1))
	}
	if *count <= 0 {
		return fmt.Errorf("crons next: --count must be positive, got %d", *count)
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsNext.Path)
	if err != nil {
		return err
	}
	if *on != "" {
		return runCronsNextProfile(*on, fs.Arg(0), format, *count)
	}
	session, release, err := openCrons(*nowFlag)
	if err != nil {
		return fmt.Errorf("crons next: %w", err)
	}
	defer release()

	ctx := context.Background()
	var wanted []crons.Row
	if fs.NArg() == 1 {
		sched, rerr := session.svc.Resolve(ctx, fs.Arg(0))
		if rerr != nil {
			return rerr
		}
		row, _, serr := session.svc.Show(ctx, sched.ID, 1)
		if serr != nil {
			return fmt.Errorf("crons next: %w", serr)
		}
		wanted = []crons.Row{row}
	} else {
		rows, lerr := session.svc.List(ctx)
		if lerr != nil {
			return fmt.Errorf("crons next: %w", lerr)
		}
		for _, r := range rows {
			if r.State == crons.StateArmed {
				wanted = append(wanted, r)
			}
		}
	}

	var out []cronsUpcoming
	for _, r := range wanted {
		instants, uerr := session.svc.Upcoming(ctx, r.ID, *count)
		if uerr != nil {
			return fmt.Errorf("crons next: %w", uerr)
		}
		for _, at := range instants {
			out = append(out, cronsUpcoming{Schedule: r.ID, Name: r.Display, At: at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > *count {
		out = out[:*count]
	}
	return renderCronsNext(os.Stdout, out, session.now(), format)
}

func renderCronsNext(w io.Writer, out []cronsUpcoming, now time.Time, format string) error {
	switch format {
	case "json":
		return ndjson.Write(w, out)
	case "plain":
		for _, u := range out {
			fmt.Fprintf(w, "%s\t%s\n", u.Name, u.At.Format(time.RFC3339))
		}
		return nil
	}
	if len(out) == 0 {
		fmt.Fprintln(w, "no armed schedule matches again")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tIN\tNAME")
	for _, u := range out {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", cronAbsTime(u.At, u.At.Location()),
			strings.TrimPrefix(cronRelTime(now, u.At), "in "), u.Name)
	}
	return tw.Flush()
}
