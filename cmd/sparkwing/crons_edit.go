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

func runCronsDisarm(args []string) error {
	fs := flag.NewFlagSet(cmdCronsDisarm.Path, flag.ContinueOnError)
	outFmt := cronsOutputFlag(fs)
	if err := parseAndCheck(cmdCronsDisarm, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		PrintHelp(cmdCronsDisarm, os.Stderr)
		return errors.New("crons disarm: one schedule name is required")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsDisarm.Path)
	if err != nil {
		return err
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons disarm: %w", err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if err := session.svc.Disarm(ctx, sched.ID); err != nil {
		return fmt.Errorf("crons disarm: %w", err)
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

func runCronsLock(args []string) error {
	fs := flag.NewFlagSet(cmdCronsLock.Path, flag.ContinueOnError)
	outFmt := cronsOutputFlag(fs)
	if err := parseAndCheck(cmdCronsLock, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		PrintHelp(cmdCronsLock, os.Stderr)
		return errors.New("crons lock: one schedule name is required")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsLock.Path)
	if err != nil {
		return err
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons lock: %w", err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if _, err := session.svc.Lock(ctx, sched.ID, cronsProver(false)); err != nil {
		return fmt.Errorf("crons lock: %w", err)
	}
	return emitCronsRow(ctx, session, sched.ID, format, "pinned")
}

func runCronsUnlock(args []string) error {
	fs := flag.NewFlagSet(cmdCronsUnlock.Path, flag.ContinueOnError)
	outFmt := cronsOutputFlag(fs)
	if err := parseAndCheck(cmdCronsUnlock, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		PrintHelp(cmdCronsUnlock, os.Stderr)
		return errors.New("crons unlock: one schedule name is required")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsUnlock.Path)
	if err != nil {
		return err
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons unlock: %w", err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if _, err := session.svc.Unlock(ctx, sched.ID); err != nil {
		return fmt.Errorf("crons unlock: %w", err)
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
	outFmt := cronsOutputFlag(fs)
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
	if fields.Empty() {
		PrintHelp(cmdCronsSet, os.Stderr)
		return errors.New("crons set: name at least one of --cron, --tz, --overlap, --catch-up or --arg")
	}

	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons set: %w", err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	row, err := session.svc.SetOverride(ctx, sched.ID, fields)
	if err != nil {
		return fmt.Errorf("crons set: %w", err)
	}
	return renderCronsOverride(row, format)
}

func runCronsReset(args []string) error {
	fs := flag.NewFlagSet(cmdCronsReset.Path, flag.ContinueOnError)
	outFmt := cronsOutputFlag(fs)
	if err := parseAndCheck(cmdCronsReset, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		PrintHelp(cmdCronsReset, os.Stderr)
		return errors.New("crons reset: one schedule name is required")
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdCronsReset.Path)
	if err != nil {
		return err
	}
	session, release, err := openCrons("")
	if err != nil {
		return fmt.Errorf("crons reset: %w", err)
	}
	defer release()

	ctx := context.Background()
	sched, err := session.svc.Resolve(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	row, err := session.svc.ClearOverride(ctx, sched.ID)
	if err != nil {
		return fmt.Errorf("crons reset: %w", err)
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
		row.Display, eff.Cron, cronsZoneLabel(eff.TZ), eff.Overlap, eff.CatchUp)
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
