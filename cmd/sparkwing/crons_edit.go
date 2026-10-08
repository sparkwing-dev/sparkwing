package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type cronsDisarmReport struct {
	Schedule string `json:"schedule"`
	Name     string `json:"name"`
}

func runCronsDisarmOne(name, format, profileName string) error {
	if profileName != "" {
		return runCronsDisarmProfile(profileName, name, format)
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons uninstall: %w", err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, name)
	if err != nil {
		return err
	}
	if err := session.svc.Disarm(ctx, sched.ID); err != nil {
		return fmt.Errorf("crons uninstall: %w", err)
	}
	report := cronsDisarmReport{Schedule: sched.ID, Name: crons.DisplayName(sched)}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(report)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, report.Name)
		return perr
	}
	fmt.Fprintf(os.Stdout, "disarmed %s; its history and its pinned binary went with it\n", report.Name)
	return nil
}

func runCronsPin(session *cronsSession, name, format string, pin bool) error {
	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, name)
	if err != nil {
		return err
	}
	if pin {
		if _, err := session.svc.Lock(ctx, sched.ID, cronsProver(false)); err != nil {
			return fmt.Errorf("crons set --pin: %w", err)
		}
		return emitCronsRow(ctx, session, sched.ID, format, "pinned")
	}
	if _, err := session.svc.Unlock(ctx, sched.ID); err != nil {
		return fmt.Errorf("crons set --unpin: %w", err)
	}
	return emitCronsRow(ctx, session, sched.ID, format, "unpinned")
}

func runCronsSet(args []string) error {
	fs := flag.NewFlagSet(cmdCronsSet.Path, flag.ContinueOnError)
	cron := fs.String("cron", "", "cron expression to run instead of the declared one")
	tz := fs.String("tz", "", "zone the expression is read in")
	overlap := fs.String("overlap", "", "what a due instant does while the previous run is going: skip|queue")
	catchUp := fs.String("catch-up", "", "how late a due instant may still fire, such as 6h")
	argsFlag := fs.StringArray("arg", nil, "argument the launch passes, k=v (repeatable; replaces the declared set)")
	// safety: each action acts on the schedule as a whole, so it stands alone
	// on a call instead of mixing with an override edit.
	actionFlags := []struct {
		name string
		set  *bool
	}{
		{"pin", fs.Bool("pin", false, "pin the schedule to the checkout as it stands")},
		{"unpin", fs.Bool("unpin", false, "let the schedule follow the checkout again")},
		{"pause", fs.Bool("pause", false, "stop the schedule firing, keeping it armed")},
		{"resume", fs.Bool("resume", false, "let a paused schedule fire again")},
		{"reset", fs.Bool("reset", false, "drop this host's override")},
	}
	outFmt := cronsOutputFlag(fs)
	on := addCronsProfileFlag(fs)
	if err := parseAndCheck(cmdCronsSet, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		PrintHelp(cmdCronsSet, os.Stderr)
		return errors.New("crons set: one schedule name is required")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsSet.Path)
	if err != nil {
		return err
	}
	fields := crons.Override{}
	if fs.Changed("cron") {
		fields.Cron = cron
	}
	if fs.Changed("tz") {
		fields.TZ = tz
	}
	if fs.Changed("overlap") {
		fields.Overlap = overlap
	}
	if fs.Changed("catch-up") {
		d, perr := time.ParseDuration(*catchUp)
		if perr != nil {
			return fmt.Errorf("crons set: --catch-up %q: expected a duration such as 6h or 30m", *catchUp)
		}
		fields.CatchUp = &d
	}
	if fs.Changed("arg") {
		parsed, perr := parseCronArgs(*argsFlag)
		if perr != nil {
			return fmt.Errorf("crons set: %w", perr)
		}
		fields.Args = parsed
	}
	var actions []string
	for _, a := range actionFlags {
		if *a.set {
			actions = append(actions, "--"+a.name)
		}
	}
	switch {
	case len(actions) > 1 || (len(actions) == 1 && !fields.Empty()):
		return fmt.Errorf("crons set: %s each stand alone; "+
			"--cron, --tz, --overlap, --catch-up and --arg edit the override in a call of their own",
			strings.Join(actions, ", "))
	case len(actions) == 0 && fields.Empty():
		PrintHelp(cmdCronsSet, os.Stderr)
		return errors.New("crons set: name at least one of --cron, --tz, --overlap, --catch-up or --arg, " +
			"or one of --pin, --unpin, --pause, --resume or --reset")
	}
	name := fs.Arg(0)
	action := ""
	if len(actions) == 1 {
		action = actions[0]
	}
	if *on != "" {
		switch action {
		case "--pin", "--unpin":
			return cronsRemotePinError(action)
		case "--pause", "--resume":
			return runCronsPauseResumeProfile(*on, name, format, action == "--pause")
		case "--reset":
			return runCronsResetProfile(*on, name, format)
		}
		return runCronsSetProfile(*on, name, format, cronsOverrideRequest(fields))
	}

	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons set: %w", err)
	}
	defer release()

	switch action {
	case "--pin", "--unpin":
		return runCronsPin(session, name, format, action == "--pin")
	case "--pause", "--resume":
		return runCronsPauseResume(session, name, format, action == "--pause")
	case "--reset":
		return runCronsReset(session, name, format)
	}
	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, name)
	if err != nil {
		return err
	}
	row, err := session.svc.SetOverride(ctx, sched.ID, fields)
	if err != nil {
		return fmt.Errorf("crons set: %w", err)
	}
	return renderCronsOverride(row, format)
}

func runCronsReset(session *cronsSession, name, format string) error {
	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, name)
	if err != nil {
		return err
	}
	row, err := session.svc.ClearOverride(ctx, sched.ID)
	if err != nil {
		return fmt.Errorf("crons set --reset: %w", err)
	}
	return renderCronsOverride(row, format)
}

func parseCronArgs(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range pairs {
		for _, one := range strings.Split(pair, ",") {
			one = strings.TrimSpace(one)
			if one == "" {
				continue
			}
			key, value, ok := strings.Cut(one, "=")
			key = strings.TrimSpace(key)
			if !ok || key == "" {
				return nil, fmt.Errorf("--arg %q: expected k=v", one)
			}
			out[key] = value
		}
	}
	return out, nil
}

func renderCronsOverride(row crons.Row, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(row)
	case "plain":
		_, err := fmt.Fprintln(os.Stdout, row.ID)
		return err
	}
	eff := row.Effective
	fmt.Fprintf(os.Stdout, "%s now runs %s %s, overlap %s, catch up %s\n",
		row.Display, eff.Cron, cronsZoneLabel(eff.TZ), eff.Overlap, cronsCatchUpLabel(row))
	if len(eff.Args) > 0 {
		fmt.Fprintf(os.Stdout, "  args: %s\n", renderArgs(eff.Args))
	}
	if len(row.OverrideFields) == 0 {
		fmt.Fprintln(os.Stdout, "  no override: this is what the repo declares")
		return nil
	}
	fmt.Fprintf(os.Stdout, "  overridden here: %s\n", strings.Join(row.OverrideFields, ", "))
	return nil
}

func cronsZoneLabel(tz string) string {
	if tz == "" {
		return "UTC"
	}
	return tz
}

func emitCronsRow(ctx context.Context, session *cronsSession, id, format, verb string) error {
	row, _, err := session.svc.Show(ctx, id, 1)
	if err != nil {
		return err
	}
	switch format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(row)
	case "plain":
		_, perr := fmt.Fprintln(os.Stdout, row.ID)
		return perr
	}
	fmt.Fprintf(os.Stdout, "%s %s (%s)\n", verb, row.Display, cronsLockWord(row))
	return nil
}

func cronsLockWord(row crons.Row) string {
	ref := row.Lock.ShortRef()
	if ref == "" {
		ref = "pinned"
	}
	switch row.Lock.State {
	case crons.LockFollows:
		return crons.LockFollows
	case crons.LockMissing:
		return crons.LockMissing
	case crons.LockAhead:
		return ref + " ahead"
	case crons.LockDirty:
		return ref + " dirty"
	}
	return ref
}

func cronsShowLock(row crons.Row) string {
	switch row.Lock.State {
	case crons.LockFollows:
		return "follows the checkout: every fire compiles it"
	case crons.LockAhead:
		return fmt.Sprintf("pinned at %s; the checkout has moved on, so `sparkwing crons install` re-pins it",
			row.Lock.ShortRef())
	case crons.LockDirty:
		return fmt.Sprintf("pinned at %s; the checkout has uncommitted edits the pin does not carry",
			row.Lock.ShortRef())
	case crons.LockMissing:
		return "pinned, but the binary is gone; `sparkwing crons install` pins the checkout as it stands"
	}
	if row.Lock.Ref == "" {
		return "pinned"
	}
	return "pinned at " + row.Lock.ShortRef()
}

func cronsArgsLabel(args map[string]string) string {
	if len(args) == 0 {
		return "-"
	}
	return renderArgs(args)
}

func cronsCatchUpLabel(row crons.Row) string {
	if row.CatchUpClamped {
		return row.Effective.CatchUp.String() + " (clamped)"
	}
	return row.Effective.CatchUp.String()
}

// safety: the store keeps an override's own fields, so a renderer has to name
// which of them the host set rather than diffing the effective cadence.
func cronsOverrideValue(o *store.CronOverride, field string) string {
	if o == nil {
		return "-"
	}
	switch field {
	case "cron":
		return dashIfEmpty(o.Cron)
	case "tz":
		return dashIfEmpty(o.TZ)
	case "overlap":
		return dashIfEmpty(o.Overlap)
	case "catch_up":
		if o.CatchUp == nil {
			return "-"
		}
		return o.CatchUp.String()
	case "args":
		if o.Args == nil {
			return "-"
		}
		return cronsArgsLabel(o.Args)
	}
	return "-"
}
