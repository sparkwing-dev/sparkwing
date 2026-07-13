package opsview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// doctorRunOrphanGrace is how long a running run row must have gone without a
// heartbeat before doctor treats it as orphaned. A live run heartbeats well
// inside this window, so the grace keeps a briefly-busy run from being
// finalized out from under itself.
const doctorRunOrphanGrace = 2 * time.Minute

// DoctorReport is what a doctor sweep found and repaired, and the wire shape
// of its -o json output.
type DoctorReport struct {
	// DryRun reports that nothing was changed; the counts are what would have
	// been repaired.
	DryRun bool `json:"dry_run"`
	// OrphanedRuns are run ids that were marked running with no live process
	// and no daemon lease, finalized as interrupted.
	OrphanedRuns []string `json:"orphaned_runs,omitempty"`
	// LegacyBoxSlotFilesRemoved counts lock files cleared from an idle legacy
	// box-slot directory.
	LegacyBoxSlotFilesRemoved int `json:"legacy_box_slot_files_removed"`
	// LiveLegacyHolders are box-slot locks still held by an older-pinned
	// pipeline binary admitting outside the daemon -- reported, never removed.
	LiveLegacyHolders []DoctorLegacyHolder `json:"live_legacy_holders,omitempty"`
	// DeadConcurrencyHolders and DeadConcurrencyWaiters count local-scope
	// concurrency rows removed because their run has ended.
	DeadConcurrencyHolders int `json:"dead_concurrency_holders"`
	DeadConcurrencyWaiters int `json:"dead_concurrency_waiters"`
	// DanglingRunDirs are run artifact directories removed because their run
	// row no longer exists.
	DanglingRunDirs []string `json:"dangling_run_dirs,omitempty"`
}

// DoctorLegacyHolder is one live legacy box-slot holder in the report.
type DoctorLegacyHolder struct {
	PID   int    `json:"pid"`
	RunID string `json:"run_id,omitempty"`
	Lock  string `json:"lock"`
}

// Clean reports whether the sweep found nothing to repair and no legacy binary
// admitting outside the daemon.
func (r DoctorReport) Clean() bool {
	return len(r.OrphanedRuns) == 0 &&
		r.LegacyBoxSlotFilesRemoved == 0 &&
		len(r.LiveLegacyHolders) == 0 &&
		r.DeadConcurrencyHolders == 0 &&
		r.DeadConcurrencyWaiters == 0 &&
		len(r.DanglingRunDirs) == 0
}

// Diagnose runs every doctor check against the sparkwing home and repairs what
// it safely can (unless dryRun). It never returns early on a single check's
// failure so a healthy check still reports even if another errors.
func Diagnose(ctx context.Context, p paths.Paths, home string, dryRun bool) (DoctorReport, error) {
	report := DoctorReport{DryRun: dryRun}

	daemonLive := liveDaemonRuns(ctx, home)

	boxHolders, err := boxslot.Holders(p.BoxSlotDir())
	if err != nil {
		return report, err
	}
	legacyRuns := map[string]struct{}{}
	for _, h := range boxHolders {
		if h.Live && h.RunID != "" {
			legacyRuns[h.RunID] = struct{}{}
		}
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		return report, err
	}
	defer func() { _ = st.Close() }()

	if err := diagnoseOrphanRuns(ctx, st, daemonLive, legacyRuns, dryRun, &report); err != nil {
		return report, err
	}
	if err := diagnoseLegacyBoxSlots(p, boxHolders, dryRun, &report); err != nil {
		return report, err
	}
	if err := diagnoseDeadConcurrency(ctx, st, dryRun, &report); err != nil {
		return report, err
	}
	if err := diagnoseDanglingRunDirs(ctx, st, p, dryRun, &report); err != nil {
		return report, err
	}
	return report, nil
}

// liveDaemonRuns returns the set of run ids the local admission daemon is
// holding or queueing, so orphan detection never finalizes a run the daemon
// still tracks. An absent daemon means no live leases, so the set is empty.
func liveDaemonRuns(ctx context.Context, home string) map[string]struct{} {
	live := map[string]struct{}{}
	qs, err := wingdclient.Query(ctx, wingdclient.Options{Home: home})
	if err != nil {
		return live
	}
	for _, h := range qs.Holders {
		live[h.RunID] = struct{}{}
	}
	for _, w := range qs.Waiters {
		live[w.RunID] = struct{}{}
	}
	return live
}

func diagnoseOrphanRuns(ctx context.Context, st *store.Store, daemonLive, legacyRuns map[string]struct{}, dryRun bool, report *DoctorReport) error {
	running, err := st.ListRuns(ctx, store.RunFilter{Statuses: []string{"running"}, Limit: 1000})
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-doctorRunOrphanGrace)
	for _, r := range running {
		if _, ok := daemonLive[r.ID]; ok {
			continue
		}
		if _, ok := legacyRuns[r.ID]; ok {
			continue
		}
		anchor := r.StartedAt
		if r.LastHeartbeatAt != nil {
			anchor = *r.LastHeartbeatAt
		}
		if anchor.IsZero() || !anchor.Before(cutoff) {
			continue
		}
		report.OrphanedRuns = append(report.OrphanedRuns, r.ID)
		if dryRun {
			continue
		}
		if err := st.FinishRun(ctx, r.ID, "cancelled",
			"interrupted: no live process or daemon lease (finalized by sparkwing doctor)"); err != nil {
			return err
		}
	}
	return nil
}

func diagnoseLegacyBoxSlots(p paths.Paths, holders []boxslot.Holder, dryRun bool, report *DoctorReport) error {
	dir := p.BoxSlotDir()
	for _, h := range holders {
		if h.Live {
			report.LiveLegacyHolders = append(report.LiveLegacyHolders, DoctorLegacyHolder{
				PID: h.PID, RunID: h.RunID, Lock: h.Path,
			})
		}
	}
	if len(report.LiveLegacyHolders) > 0 {
		return nil
	}
	if dryRun {
		n, err := countDirFiles(dir)
		if err != nil {
			return err
		}
		report.LegacyBoxSlotFilesRemoved = n
		return nil
	}
	removed, live, err := boxslot.PurgeIfIdle(dir)
	if err != nil {
		return err
	}
	if len(live) > 0 {
		return nil
	}
	report.LegacyBoxSlotFilesRemoved = removed
	return nil
}

func countDirFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n, nil
}

func diagnoseDeadConcurrency(ctx context.Context, st *store.Store, dryRun bool, report *DoctorReport) error {
	if dryRun {
		h, w, err := st.CountDeadLocalConcurrency(ctx)
		if err != nil {
			return err
		}
		report.DeadConcurrencyHolders, report.DeadConcurrencyWaiters = h, w
		return nil
	}
	h, w, err := st.PurgeDeadLocalConcurrency(ctx)
	if err != nil {
		return err
	}
	report.DeadConcurrencyHolders, report.DeadConcurrencyWaiters = h, w
	return nil
}

func diagnoseDanglingRunDirs(ctx context.Context, st *store.Store, p paths.Paths, dryRun bool, report *DoctorReport) error {
	entries, err := os.ReadDir(p.RunsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_, err := st.GetRun(ctx, e.Name())
		if err == nil {
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		report.DanglingRunDirs = append(report.DanglingRunDirs, e.Name())
		if dryRun {
			continue
		}
		if err := os.RemoveAll(filepath.Join(p.RunsDir(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// RenderDoctor writes r in the requested format: "json", "plain", or pretty.
// legacyLine, when non-empty, is the legacy-coexistence warning appended to
// the pretty view (the caller owns the legacy-count phrasing).
func RenderDoctor(w io.Writer, r DoctorReport, format, legacyLine string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "plain":
		return renderDoctorPlain(w, r)
	default:
		return renderDoctorPretty(w, r, legacyLine)
	}
}

func renderDoctorPlain(w io.Writer, r DoctorReport) error {
	fmt.Fprintf(w, "orphaned_runs\t%d\n", len(r.OrphanedRuns))
	fmt.Fprintf(w, "legacy_box_slot_files_removed\t%d\n", r.LegacyBoxSlotFilesRemoved)
	fmt.Fprintf(w, "live_legacy_holders\t%d\n", len(r.LiveLegacyHolders))
	fmt.Fprintf(w, "dead_concurrency_holders\t%d\n", r.DeadConcurrencyHolders)
	fmt.Fprintf(w, "dead_concurrency_waiters\t%d\n", r.DeadConcurrencyWaiters)
	fmt.Fprintf(w, "dangling_run_dirs\t%d\n", len(r.DanglingRunDirs))
	return nil
}

func renderDoctorPretty(w io.Writer, r DoctorReport, legacyLine string) error {
	verb, would := "removed", ""
	if r.DryRun {
		verb, would = "found", " (dry run: nothing changed)"
	}
	if r.Clean() {
		fmt.Fprintf(w, "healthy: nothing to repair%s\n", would)
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if n := len(r.OrphanedRuns); n > 0 {
		fmt.Fprintf(tw, "orphaned runs finalized\t%d\n", n)
	}
	if r.LegacyBoxSlotFilesRemoved > 0 {
		fmt.Fprintf(tw, "legacy box-slot files %s\t%d\n", verb, r.LegacyBoxSlotFilesRemoved)
	}
	if r.DeadConcurrencyHolders > 0 || r.DeadConcurrencyWaiters > 0 {
		fmt.Fprintf(tw, "dead local concurrency rows %s\t%d holders, %d waiters\n",
			verb, r.DeadConcurrencyHolders, r.DeadConcurrencyWaiters)
	}
	if n := len(r.DanglingRunDirs); n > 0 {
		fmt.Fprintf(tw, "dangling run directories %s\t%d\n", verb, n)
	}
	_ = tw.Flush()

	if legacyLine != "" {
		fmt.Fprintf(w, "\nwarning: %s\n", legacyLine)
		for _, h := range r.LiveLegacyHolders {
			fmt.Fprintf(w, "  pid %d holding %s\n", h.PID, h.Lock)
		}
	}
	return nil
}
