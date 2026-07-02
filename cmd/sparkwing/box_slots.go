// `sparkwing box-slots` -- inspect and live-tune the host-local
// run-concurrency semaphore. The cap is a host control runs re-read
// while they queue or hold, so an operator can rebalance concurrency
// without restarting in-flight work.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

// boxSlotReport is the wire shape of `box-slots show` and the
// post-mutation echo of `box-slots set`.
type boxSlotReport struct {
	Cap           int    `json:"cap"`
	Disabled      bool   `json:"disabled"`
	Source        string `json:"source"`
	ActiveHolders int    `json:"active_holders"`
	Waiters       int    `json:"waiters"`
}

func runBoxSlots(args []string) error {
	if len(args) == 0 {
		PrintHelp(cmdBoxSlots, os.Stderr)
		return errors.New("box-slots: missing subcommand")
	}
	switch args[0] {
	case "show":
		return runBoxSlotsShow(args[1:])
	case "set":
		return runBoxSlotsSet(args[1:])
	case "list":
		return runBoxSlotsList(args[1:])
	case "release":
		return runBoxSlotsRelease(args[1:])
	case "help", "-h", "--help":
		PrintHelp(cmdBoxSlots, os.Stdout)
		return nil
	default:
		PrintHelp(cmdBoxSlots, os.Stderr)
		return fmt.Errorf("box-slots: unknown verb %q (valid: show, set, list, release)", args[0])
	}
}

func runBoxSlotsShow(args []string) error {
	fs := flag.NewFlagSet(cmdBoxSlotsShow.Path, flag.ContinueOnError)
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdBoxSlotsShow, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(*outFmt, cmdBoxSlotsShow.Path)
	if err != nil {
		return err
	}
	report, err := boxSlotReportNow()
	if err != nil {
		return err
	}
	return renderBoxSlotReport(report, format)
}

func runBoxSlotsSet(args []string) error {
	fs := flag.NewFlagSet(cmdBoxSlotsSet.Path, flag.ContinueOnError)
	to := fs.String("to", "", "new cap: a positive integer, 'off', or 'default'")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdBoxSlotsSet, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(*outFmt, cmdBoxSlotsSet.Path)
	if err != nil {
		return err
	}
	if *to == "" {
		return errors.New("box-slots set: --to is required (a positive integer, 'off', or 'default')")
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	if err := applyBoxSlotControl(paths.BoxSlotDir(), *to); err != nil {
		return err
	}
	report, err := boxSlotReportNow()
	if err != nil {
		return err
	}
	return renderBoxSlotReport(report, format)
}

// applyBoxSlotControl validates a `set --to` value and writes (or
// clears) the live host control. Integers <= 0 and the off-aliases
// disable the semaphore; "default" reverts to the env/heuristic.
func applyBoxSlotControl(lockDir, value string) error {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "default":
		return boxslot.ClearControl(lockDir)
	case "off", "none", "disable", "disabled":
		return boxslot.WriteControl(lockDir, "off")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("box-slots set: --to must be a positive integer, 'off', or 'default', got %q", value)
	}
	if n <= 0 {
		return boxslot.WriteControl(lockDir, "off")
	}
	return boxslot.WriteControl(lockDir, strconv.Itoa(n))
}

func boxSlotReportNow() (boxSlotReport, error) {
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return boxSlotReport{}, err
	}
	cap, source := orchestrator.HostBoxSlotCap(paths)
	stat, err := boxslot.Status(paths.BoxSlotDir())
	if err != nil {
		return boxSlotReport{}, err
	}
	return boxSlotReport{
		Cap:           cap,
		Disabled:      cap <= 0,
		Source:        source,
		ActiveHolders: stat.ActiveHolders,
		Waiters:       stat.Waiters,
	}, nil
}

// boxSlotHolderRow is the wire shape of one `box-slots list` row. Zero
// pid / claimed_at mean the marker filename didn't carry the standard
// shape; run_id is empty until the owning run annotates its marker.
type boxSlotHolderRow struct {
	PID       int    `json:"pid"`
	ClaimedAt string `json:"claimed_at"`
	RunID     string `json:"run_id,omitempty"`
	Live      bool   `json:"live"`
	Lock      string `json:"lock"`
}

func runBoxSlotsList(args []string) error {
	fs := flag.NewFlagSet(cmdBoxSlotsList.Path, flag.ContinueOnError)
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdBoxSlotsList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(*outFmt, cmdBoxSlotsList.Path)
	if err != nil {
		return err
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	holders, err := boxslot.Holders(paths.BoxSlotDir())
	if err != nil {
		return err
	}
	rows := make([]boxSlotHolderRow, 0, len(holders))
	for _, h := range holders {
		row := boxSlotHolderRow{
			PID:   h.PID,
			RunID: h.RunID,
			Live:  h.Live,
			Lock:  h.Path,
		}
		if !h.ClaimedAt.IsZero() {
			row.ClaimedAt = h.ClaimedAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return renderBoxSlotHolders(rows, format)
}

func renderBoxSlotHolders(rows []boxSlotHolderRow, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case "plain":
		for _, r := range rows {
			fmt.Printf("%d\t%s\t%s\t%s\t%s\n",
				r.PID, orDash(r.ClaimedAt), orDash(r.RunID), liveWord(r.Live), r.Lock)
		}
		return nil
	default:
		if len(rows) == 0 {
			fmt.Println("no box-slot holders")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PID\tCLAIMED\tRUN\tSTATE\tLOCK")
		for _, r := range rows {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n",
				r.PID, orDash(r.ClaimedAt), orDash(r.RunID), liveWord(r.Live), r.Lock)
		}
		return w.Flush()
	}
}

func liveWord(live bool) string {
	if live {
		return "live"
	}
	return "stale"
}

// boxSlotReleaseReport is the wire shape of `box-slots release`.
type boxSlotReleaseReport struct {
	Released string `json:"released"`
	Forced   bool   `json:"forced"`
}

func runBoxSlotsRelease(args []string) error {
	fs := flag.NewFlagSet(cmdBoxSlotsRelease.Path, flag.ContinueOnError)
	force := fs.Bool("force", false, "SIGKILL a live holder's owner before removing its marker")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdBoxSlotsRelease, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(*outFmt, cmdBoxSlotsRelease.Path)
	if err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("box-slots release: exactly one <lockfile> basename required (see `box-slots list`)")
	}
	name := fs.Args()[0]
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	if err := boxslot.ReleaseHolder(paths.BoxSlotDir(), name, *force); err != nil {
		if errors.Is(err, boxslot.ErrHolderLive) {
			return fmt.Errorf(
				"box-slots release: %s is held by a live process; re-run with --force to SIGKILL its owner", name)
		}
		return fmt.Errorf("box-slots release: %w", err)
	}
	return renderBoxSlotRelease(boxSlotReleaseReport{Released: name, Forced: *force}, format)
}

func renderBoxSlotRelease(r boxSlotReleaseReport, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "plain":
		fmt.Printf("released\t%s\n", r.Released)
		return nil
	default:
		fmt.Printf("released %s\n", r.Released)
		return nil
	}
}

func renderBoxSlotReport(r boxSlotReport, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "plain":
		fmt.Printf("cap\t%d\n", r.Cap)
		fmt.Printf("disabled\t%t\n", r.Disabled)
		fmt.Printf("source\t%s\n", r.Source)
		fmt.Printf("active_holders\t%d\n", r.ActiveHolders)
		fmt.Printf("waiters\t%d\n", r.Waiters)
		return nil
	default:
		capStr := strconv.Itoa(r.Cap)
		if r.Disabled {
			capStr = "disabled (unlimited)"
		}
		fmt.Printf("box slots: %s  (source: %s)\n", capStr, r.Source)
		fmt.Printf("  active holders: %d\n", r.ActiveHolders)
		fmt.Printf("  waiters:        %d\n", r.Waiters)
		return nil
	}
}
