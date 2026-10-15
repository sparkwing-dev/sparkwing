package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type scheduleOptions struct {
	format      string
	fires, next int
	override    controllerwire.ScheduleOverride
	action      string
}

func parseScheduleOptions(inv clisurface.Invocation) (scheduleOptions, error) {
	opts := scheduleOptions{format: inv.Output(color.IsInteractiveStdout()), fires: 10}
	invalid := func(message string) (scheduleOptions, error) {
		return opts, &clisurface.UsageError{Path: inv.Command.Path, Message: message}
	}
	for name, target := range map[string]*int{"fires": &opts.fires, "next": &opts.next} {
		if inv.Has(name) {
			n, err := strconv.Atoi(inv.Value(name))
			if err != nil || n < 0 || name == "next" && n == 0 {
				qualifier := "nonnegative"
				if name == "next" {
					qualifier = "positive"
				}
				return invalid("--" + name + " requires a " + qualifier + " integer")
			}
			*target = n
		}
	}
	reports := 0
	for _, name := range []string{"all", "timer", "next"} {
		if inv.Bool(name) || name == "next" && inv.Has(name) {
			reports++
		}
	}
	if strings.HasSuffix(inv.Command.Path, " list") && reports > 1 {
		return invalid("--all, --timer and --next select separate reports")
	}
	if inv.Has("next") && inv.Has("fires") {
		return invalid("--next and --fires select separate reports")
	}
	if inv.Bool("all-repos") && inv.Has("schedule") {
		return invalid("--all-repos and --schedule select separate scopes")
	}
	if !strings.HasSuffix(inv.Command.Path, " set") {
		return opts, nil
	}
	for _, name := range []string{"pin", "unpin", "reset"} {
		if inv.Bool(name) {
			if opts.action != "" {
				return invalid("--pin, --unpin and --reset each stand alone")
			}
			opts.action = name
		}
	}
	for name, target := range map[string]**string{"cron": &opts.override.Cron, "tz": &opts.override.TZ, "overlap": &opts.override.Overlap, "catch-up": &opts.override.CatchUp} {
		if inv.Has(name) {
			value := inv.Value(name)
			*target = &value
		}
	}
	if inv.Has("arg") {
		args := map[string]string{}
		for _, pair := range inv.Flags["arg"] {
			if pair == "" {
				if len(inv.Flags["arg"]) != 1 {
					return invalid("an empty --arg must stand alone")
				}
				continue
			}
			key, value, ok := strings.Cut(pair, "=")
			if !ok || strings.TrimSpace(key) == "" {
				return invalid("--arg requires KEY=VALUE")
			}
			if _, exists := args[key]; exists {
				return invalid("--arg repeats an argument name")
			}
			args[key] = value
		}
		opts.override.Args = &args
	}
	hasEdit := opts.override.Cron != nil || opts.override.TZ != nil || opts.override.Overlap != nil || opts.override.CatchUp != nil || opts.override.Args != nil
	if opts.action != "" {
		if hasEdit {
			return invalid("--pin, --unpin and --reset cannot be combined with cadence edits")
		}
		return opts, nil
	}
	if err := opts.override.Validate(); err != nil {
		return invalid(err.Error())
	}
	return opts, nil
}

func runSchedules(ctx context.Context, args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"schedules"}, args...))
	if err != nil {
		return err
	}
	if inv.Help() || inv.Command.Path == "sparkwing schedules" {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	opts, err := parseScheduleOptions(inv)
	if err != nil {
		return err
	}
	switch inv.Command.Path {
	case "sparkwing schedules install":
		return runCronsInstall(args[1:])
	case "sparkwing schedules uninstall":
		if len(inv.Flags["schedule"]) == 0 {
			return runCronsUninstall(args[1:])
		}
	}
	if opts.action == "pin" || opts.action == "unpin" {
		return pinSchedules(inv, opts)
	}
	ctx, cancel := context.WithTimeout(ctx, cronsProfileTimeout)
	defer cancel()
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	manager, err := client.NewScheduleManager(options)
	if err != nil {
		return err
	}
	return manageSchedules(ctx, inv, manager, os.Stdout)
}

func manageSchedules(ctx context.Context, inv clisurface.Invocation, manager controllerwire.ScheduleManager, out io.Writer) error {
	opts, err := parseScheduleOptions(inv)
	if err != nil {
		return err
	}
	if inv.Command.Path == "sparkwing schedules list" {
		return listSchedules(ctx, inv, opts, manager, out)
	}
	rows, err := selectSchedules(ctx, inv, opts, manager)
	if err != nil {
		return err
	}
	for _, detail := range rows {
		row := detail.Schedule
		var result any = row
		plain := row.ID
		verb := strings.TrimPrefix(inv.Command.Path, "sparkwing schedules ")
		switch verb {
		case "show":
			if opts.next > 0 {
				if err := renderScheduleNext(out, detail.Upcoming, row.ID, row.Name, opts.format); err != nil {
					return err
				}
				continue
			}
			if opts.format == "pretty" {
				if err := renderScheduleDetail(out, detail); err != nil {
					return err
				}
				continue
			}
			result = detail
		case "pause", "resume":
			result, err = manager.SetSchedulePaused(ctx, row.ID, verb == "pause")
		case "set":
			if opts.action == "reset" {
				result, err = manager.ResetSchedule(ctx, row.ID)
			} else {
				result, err = manager.OverrideSchedule(ctx, row.ID, opts.override)
			}
		case "run":
			var launched controllerwire.ScheduleRun
			launched, err = manager.RunSchedule(ctx, row.ID, rand.Text())
			result = launched
			plain = launched.RunID
		case "uninstall":
			result, err = manager.DeleteSchedule(ctx, row.ID)
		default:
			return fmt.Errorf("unsupported schedule operation")
		}
		if err != nil {
			return err
		}
		if err := renderScheduleRecord(out, result, plain, opts.format); err != nil {
			return err
		}
	}
	return nil
}

func selectSchedules(ctx context.Context, inv clisurface.Invocation, opts scheduleOptions, manager controllerwire.ScheduleManager) ([]controllerwire.DetailView, error) {
	var rows []controllerwire.DetailView
	seen := map[string]bool{}
	query := controllerwire.ScheduleReadQuery{Fires: 0}
	if strings.HasSuffix(inv.Command.Path, " show") {
		query.Fires = opts.fires
		query.Next = opts.next
	}
	for _, name := range inv.Flags["schedule"] {
		detail, err := manager.ReadSchedule(ctx, name, query)
		if err != nil {
			return nil, err
		}
		if !seen[detail.Schedule.ID] {
			rows = append(rows, detail)
			seen[detail.Schedule.ID] = true
		}
	}
	return rows, nil
}

type scheduleSummary struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Pipeline      string  `json:"pipeline"`
	Where         string  `json:"where"`
	State         string  `json:"state"`
	Paused        bool    `json:"paused"`
	Cron          string  `json:"cron"`
	TZ            string  `json:"tz"`
	LockState     string  `json:"lock_state"`
	LockRef       string  `json:"lock_ref,omitempty"`
	Overridden    bool    `json:"overridden"`
	OverrideStale bool    `json:"override_stale"`
	NextDueAt     *string `json:"next_due_at,omitempty"`
	LastFiredAt   *string `json:"last_fired_at,omitempty"`
	LastOutcome   string  `json:"last_outcome,omitempty"`
}

func summarizeSchedule(row controllerwire.ScheduleView) scheduleSummary {
	return scheduleSummary{ID: row.ID, Name: row.Name, Pipeline: row.Pipeline, Where: row.Where, State: row.State, Paused: row.Paused, Cron: row.Effective.Cron, TZ: row.Effective.TZ, LockState: row.Lock.State, LockRef: row.Lock.Ref, Overridden: len(row.Override.Fields) > 0, OverrideStale: row.Override.Stale, NextDueAt: row.NextDueAt, LastFiredAt: row.LastFiredAt, LastOutcome: row.LastOutcome}
}

func listSchedules(ctx context.Context, inv clisurface.Invocation, opts scheduleOptions, manager controllerwire.ScheduleManager, out io.Writer) error {
	result, err := manager.ListSchedules(ctx, controllerwire.ScheduleQuery{All: inv.Bool("all"), Next: opts.next})
	if err != nil {
		return err
	}
	if inv.Bool("timer") {
		h := result.Health
		if opts.format == "pretty" {
			err = renderScheduleHealth(out, h)
		} else {
			err = renderScheduleRecord(out, h, h.Detail, opts.format)
		}
		if err != nil {
			return err
		}
		if h.Armed > 0 && (h.Timer.Foreign || h.Timer.Stale || h.TickStale || h.LastTick.Error != "" || !h.Timer.Enabled && h.LastTick.At == "") {
			return exitError(1, fmt.Errorf("schedule evaluator is unhealthy: %s", h.Detail))
		}
		return nil
	}
	if opts.next > 0 {
		for _, row := range result.Upcoming {
			if err := renderScheduleRecord(out, row, row.Name+"\t"+row.At, opts.format); err != nil {
				return err
			}
		}
		return nil
	}
	for _, row := range result.Schedules {
		if err := renderScheduleRecord(out, summarizeSchedule(row), row.ID, opts.format); err != nil {
			return err
		}
	}
	if len(result.Schedules) == 0 && opts.format == "pretty" {
		_, err = fmt.Fprintln(out, "no schedules are armed; run sparkwing schedules install")
	}
	return err
}

func renderScheduleRecord(out io.Writer, value any, plain, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(out).Encode(value)
	case "plain":
		_, err := fmt.Fprintln(out, plain)
		return err
	}
	switch row := value.(type) {
	case controllerwire.ScheduleView:
		return renderScheduleRecord(out, summarizeSchedule(row), row.ID, "pretty")
	case scheduleSummary:
		next := "-"
		if row.NextDueAt != nil {
			next = *row.NextDueAt
		}
		override := ""
		if row.Overridden {
			override = " override"
		}
		if row.OverrideStale {
			override += " (declaration changed)"
		}
		_, err := fmt.Fprintf(out, "%s  %s  %s  %s  %s %s  %s  next %s%s\n", row.ID, row.Name, row.Cron, row.TZ, row.LockState, row.LockRef, row.State, next, override)
		return err
	case controllerwire.ScheduleRun:
		_, err := fmt.Fprintf(out, "run %s submitted (%s)\n  follow: sparkwing runs logs --run %s --follow\n", row.RunID, row.Schedule.Name, row.RunID)
		return err
	case controllerwire.ScheduleDeletion:
		state := "already absent"
		if row.Deleted {
			state = "disarmed"
		}
		_, err := fmt.Fprintf(out, "%s: %s\n", row.ScheduleID, state)
		return err
	case controllerwire.ScheduleUpcoming:
		_, err := fmt.Fprintf(out, "%s  %s\n", row.At, row.Name)
		return err
	default:
		return fmt.Errorf("unsupported schedule display")
	}
}

func renderScheduleNext(out io.Writer, times []string, id, name, format string) error {
	for _, at := range times {
		row := controllerwire.ScheduleUpcoming{Schedule: id, Name: name, At: at}
		if err := renderScheduleRecord(out, row, name+"\t"+at, format); err != nil {
			return err
		}
	}
	return nil
}

func renderScheduleHealth(out io.Writer, h controllerwire.HealthView) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "timer\t%s\nunit\t%s\nbinary\t%s\nlast tick\t%s\ntick error\t%s\nschedules\t%d armed, %d paused, %d undeclared\nlocks\t%d pinned, %d following, %d missing\n", h.Timer.Detail, h.Timer.Path, h.Timer.Binary, h.LastTick.At, h.LastTick.Error, h.Armed, h.Paused, h.Undeclared, h.Locked, h.Following, h.MissingBinary)
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "%s\n%s\n", h.Detail, h.Remedy)
	return err
}

func renderScheduleDetail(out io.Writer, detail controllerwire.DetailView) error {
	row := detail.Schedule
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "schedule\t%s\nname\t%s\nrepo\t%s\npipeline\t%s\nwhere\t%s\nstate\t%s\nlock\t%s %s\nbinary\t%s\ndigest\t%s\nlast run\t%s\nlast outcome\t%s\n", row.ID, row.Name, row.RepoPath, row.Pipeline, row.Where, row.State, row.Lock.State, row.Lock.Ref, row.Lock.Binary, row.Lock.Digest, row.LastRunID, row.LastOutcome)
	fmt.Fprintln(tw, "FIELD\tDECLARED\tOVERRIDE\tEFFECTIVE")
	fmt.Fprintf(tw, "cron\t%s\t%s\t%s\ntz\t%s\t%s\t%s\noverlap\t%s\t%s\t%s\ncatch up\t%s\t%s\t%s\nargs\t%s\t%s\t%s\n", row.Cron, row.Override.Cron, row.Effective.Cron, row.TZ, row.Override.TZ, row.Effective.TZ, row.Overlap, row.Override.Overlap, row.Effective.Overlap, time.Duration(row.CatchUpNS), time.Duration(row.Override.CatchUpNS), time.Duration(row.Effective.CatchUpNS), renderArgs(row.Args), renderArgs(row.Override.Args), renderArgs(row.Effective.Args))
	if row.Override.Stale {
		fmt.Fprintln(tw, "override\tdeclaration changed; run sparkwing schedules install")
	}
	fmt.Fprintln(tw, "DUE\tDECIDED\tOUTCOME\tRUN\tSTATUS\tDETAIL")
	for _, fire := range detail.Fires {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", fire.DueAt, fire.DecidedAt, fire.Outcome, fire.RunID, fire.RunStatus, fire.Detail)
	}
	return tw.Flush()
}

func pinSchedules(inv clisurface.Invocation, opts scheduleOptions) error {
	if inv.Value("profile") != "" {
		return cronsRemotePinError("--" + opts.action)
	}
	session, release, err := openCrons("")
	if err != nil {
		return err
	}
	defer release()
	var ids []string
	seen := map[string]bool{}
	for _, name := range inv.Flags["schedule"] {
		row, err := session.svc.Resolve(context.Background(), name)
		if err != nil {
			return err
		}
		if !seen[row.ID] {
			ids = append(ids, row.ID)
			seen[row.ID] = true
		}
	}
	for _, id := range ids {
		if err := runCronsPin(session, id, opts.format, opts.action == "pin"); err != nil {
			return err
		}
	}
	return nil
}
