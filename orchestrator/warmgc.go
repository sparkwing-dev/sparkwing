package orchestrator

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// gcTimeout caps the whole warm-PVC sweep so a stuck RemoveAll can't
// stall claim-loop startup.
const gcTimeout = 5 * time.Second

const gcGitDirAge = 7 * 24 * time.Hour

const gcTmpFileAge = 24 * time.Hour

// gcRunDirGrace leaves room for dashboard log follow-ups without 404s.
const gcRunDirGrace = 1 * time.Hour

// GCStats summarizes what GCWarmRoot removed.
type GCStats struct {
	GitDirsRemoved    int
	TmpEntriesRemoved int
	RunDirsRemoved    int
	BytesFreed        int64
}

// TerminalRunLister is the narrow ListRuns contract GCWarmRoot needs.
type TerminalRunLister interface {
	ListRuns(ctx context.Context, f store.RunFilter) ([]*store.Run, error)
}

// GCWarmRoot prunes stale state from a warm-runner's SPARKWING_HOME.
// Sweeps <root>/git/*, <root>/tmp/*, and terminal <root>/runs/<id>/
// (skipped when ctrl is nil). Bounded by gcTimeout.
func GCWarmRoot(ctx context.Context, root string, ctrl TerminalRunLister, logger *slog.Logger) (GCStats, error) {
	if logger == nil {
		logger = slog.Default()
	}
	var stats GCStats
	if root == "" {
		return stats, errors.New("gc: root is required")
	}
	// Missing root is fine on a fresh pod.
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			logger.Info("gc: root does not exist; nothing to sweep", "root", root)
			return stats, nil
		}
		return stats, err
	}

	sweepCtx, cancel := context.WithTimeout(ctx, gcTimeout)
	defer cancel()

	now := time.Now()

	gitDirs, gitBytes := sweepAgeOldest(sweepCtx, filepath.Join(root, "git"), now.Add(-gcGitDirAge), true, logger)
	stats.GitDirsRemoved = gitDirs
	stats.BytesFreed += gitBytes

	tmpEntries, tmpBytes := sweepAgeOldest(sweepCtx, filepath.Join(root, "tmp"), now.Add(-gcTmpFileAge), false, logger)
	stats.TmpEntriesRemoved = tmpEntries
	stats.BytesFreed += tmpBytes

	if ctrl != nil {
		runDirs, runBytes := sweepTerminalRuns(sweepCtx, filepath.Join(root, "runs"), ctrl, now.Add(-gcRunDirGrace), logger)
		stats.RunDirsRemoved = runDirs
		stats.BytesFreed += runBytes
	}

	if err := sweepCtx.Err(); errors.Is(err, context.DeadlineExceeded) {
		logger.Warn("gc: timed out; returning partial stats", "stats", stats)
	}
	return stats, nil
}

// sweepAgeOldest removes mtime-before-cutoff entries; dirsOnly limits
// to directories (the git/ sweep). Per-entry errors are logged and
// skipped.
func sweepAgeOldest(ctx context.Context, dir string, cutoff time.Time, dirsOnly bool, logger *slog.Logger) (int, int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("gc: readdir failed", "dir", dir, "err", err)
		}
		return 0, 0
	}
	var removed int
	var bytes int64
	for _, e := range entries {
		if ctx.Err() != nil {
			return removed, bytes
		}
		if dirsOnly && !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			logger.Warn("gc: stat failed", "entry", e.Name(), "err", err)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		sz := entrySize(path)
		if err := os.RemoveAll(path); err != nil {
			logger.Warn("gc: remove failed", "path", path, "err", err)
			continue
		}
		removed++
		bytes += sz
	}
	return removed, bytes
}

// sweepTerminalRuns removes run dirs the controller reports terminal
// + finished before cutoff. Dirs not in the response are left alone.
func sweepTerminalRuns(ctx context.Context, runsDir string, ctrl TerminalRunLister, cutoff time.Time, logger *slog.Logger) (int, int64) {
	if _, err := os.Stat(runsDir); err != nil {
		if os.IsNotExist(err) {
			return 0, 0
		}
		logger.Warn("gc: stat runs dir failed", "dir", runsDir, "err", err)
		return 0, 0
	}
	// Single page; ephemeral PVC + time-bounded sweep.
	runs, err := ctrl.ListRuns(ctx, store.RunFilter{
		Statuses: []string{"success", "failed", "cancelled"},
		Limit:    500,
	})
	if err != nil {
		logger.Warn("gc: list runs failed; skipping run-dir sweep", "err", err)
		return 0, 0
	}
	var removed int
	var bytes int64
	for _, r := range runs {
		if ctx.Err() != nil {
			return removed, bytes
		}
		if r.FinishedAt == nil || !r.FinishedAt.Before(cutoff) {
			continue
		}
		path := filepath.Join(runsDir, r.ID)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		sz := entrySize(path)
		if err := os.RemoveAll(path); err != nil {
			logger.Warn("gc: remove run dir failed", "path", path, "err", err)
			continue
		}
		removed++
		bytes += sz
	}
	return removed, bytes
}

// entrySize walks path summing file sizes; unreadable paths count 0.
func entrySize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
