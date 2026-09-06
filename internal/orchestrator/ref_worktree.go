package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RefWorktreeRevKey names the trigger environment entry holding the commit a
// --sw-ref submission resolved to.
const RefWorktreeRevKey = "_SPARKWING_SUBMIT_REF_REV"

const refWorktreeGitTimeout = 10 * time.Second

const refWorktreeLeaseSuffix = ".lease"

// CreateRefWorktree checks commit out into a worktree owned by runID. Nothing
// here removes it: the tree outlives this process so a detached run can execute it.
func CreateRefWorktree(
	ctx context.Context, p Paths, originRepo string, commit Commit, runID string, logger *slog.Logger,
) (string, error) {
	if err := fssecure.EnsureDir(p.RefWorktreesDir()); err != nil {
		return "", fmt.Errorf("secure ref worktree directory: %w", err)
	}
	dir := p.RefWorktreeDir(runID)
	out, err := exec.CommandContext(ctx, "git", "-C", originRepo,
		"worktree", "add", "--detach", "--quiet", "--", dir, string(commit)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add %s: %w: %s", commit, err, strings.TrimSpace(string(out)))
	}
	if info, serr := os.Stat(filepath.Join(dir, ".sparkwing")); serr != nil || !info.IsDir() {
		if rerr := RemoveRefWorktree(ctx, p, dir, logger); rerr != nil {
			return "", rerr
		}
		return "", fmt.Errorf("commit %s has no .sparkwing/ directory, so no pipeline can be resolved from it", commit)
	}
	return dir, nil
}

// RemoveRefWorktree deletes a worktree and its registration. It refuses a
// directory outside the ref worktree root, so a stored path that has been
// corrupted or forged cannot name an operator's own checkout for deletion.
func RemoveRefWorktree(ctx context.Context, p Paths, dir string, logger *slog.Logger) error {
	if dir == "" {
		return nil
	}
	if !withinRefWorktrees(p, dir) {
		return fmt.Errorf("refusing to remove %s: it is outside %s", dir, p.RefWorktreesDir())
	}
	// safety: removal runs on the consumer's shutdown path, where a hung git
	// call would keep the process from exiting.
	ctx, cancel := context.WithTimeout(ctx, refWorktreeGitTimeout)
	defer cancel()
	if origin := refWorktreeOrigin(dir); origin != "" {
		out, err := exec.CommandContext(ctx, "git", "-C", origin,
			"worktree", "remove", "--force", "--", dir).CombinedOutput()
		logBestEffortGit(ctx, logger, slog.LevelWarn, "worktree remove", out, err)
		defer func() {
			pruneCtx := context.WithoutCancel(ctx)
			pout, perr := exec.CommandContext(pruneCtx, "git", "-C", origin,
				"worktree", "prune").CombinedOutput()
			logBestEffortGit(pruneCtx, logger, slog.LevelWarn, "worktree prune", pout, perr)
		}()
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove ref worktree %s: %w", dir, err)
	}
	if err := os.Remove(dir + refWorktreeLeaseSuffix); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove ref worktree lease for %s: %w", dir, err)
	}
	return nil
}

func withinRefWorktrees(p Paths, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(p.RefWorktreesDir()), filepath.Clean(dir))
	if err != nil {
		return false
	}
	// safety: the root itself is not a child, so a path resolving to it removes
	// every live run's worktree rather than one.
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func refWorktreeOrigin(dir string) string {
	gitDir := gitDirPointer(filepath.Join(dir, ".git"), dir)
	marker := string(filepath.Separator) + ".git" + string(filepath.Separator) + "worktrees" + string(filepath.Separator)
	if i := strings.Index(gitDir, marker); i >= 0 {
		return gitDir[:i]
	}
	return ""
}

// SweepRefWorktrees reclaims worktrees whose triggers have finished, or whose
// submissions never wrote one, and reports how many it removed.
func SweepRefWorktrees(ctx context.Context, p Paths, st *store.Store, logger *slog.Logger) (int, error) {
	entries, err := os.ReadDir(p.RefWorktreesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read ref worktree directory: %w", err)
	}
	removed := 0
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasSuffix(entry.Name(), refWorktreeLeaseSuffix) {
			continue
		}
		done, rerr := reclaimRefWorktree(ctx, p, st, entry.Name(), logger)
		if rerr != nil {
			failures = append(failures, fmt.Errorf("ref worktree %s: %w", entry.Name(), rerr))
			continue
		}
		if done {
			removed++
		}
	}
	return removed, errors.Join(failures...)
}

func reclaimRefWorktree(
	ctx context.Context, p Paths, st *store.Store, runID string, logger *slog.Logger,
) (bool, error) {
	settled, err := refWorktreeTriggerSettled(ctx, st, runID)
	if err != nil || !settled {
		return false, err
	}
	hold, held, herr := HoldRefWorktree(p, runID)
	if herr != nil || !held {
		return false, herr
	}
	defer func() {
		if rerr := ReleaseRefWorktree(hold); rerr != nil && logger != nil {
			logger.Warn("release worktree hold", "run_id", runID, "error", rerr)
		}
	}()
	if rerr := RemoveRefWorktree(ctx, p, p.RefWorktreeDir(runID), logger); rerr != nil {
		return false, rerr
	}
	return true, nil
}

func refWorktreeTriggerSettled(ctx context.Context, st *store.Store, runID string) (bool, error) {
	trig, err := st.GetTrigger(ctx, runID)
	if err == nil {
		return trig.IsFinished(), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	return true, nil
}

func refWorktreeLeasePath(p Paths, runID string) string {
	return filepath.Join(p.RefWorktreesDir(), runID+refWorktreeLeaseSuffix)
}

// HoldRefWorktree marks a worktree in use for as long as the returned file
// stays open, and reports false when another process already holds it. The
// operating system releases the hold when its holder dies, so a worktree a
// crashed process was writing becomes reclaimable without waiting out a timer.
func HoldRefWorktree(p Paths, runID string) (*os.File, bool, error) {
	if runID == "" || runID != filepath.Base(runID) || runID == "." || runID == ".." {
		return nil, false, fmt.Errorf("run id %q does not name one directory under %s",
			runID, p.RefWorktreesDir())
	}
	if err := fssecure.EnsureDir(p.RefWorktreesDir()); err != nil {
		return nil, false, fmt.Errorf("secure ref worktree directory: %w", err)
	}
	file, err := os.OpenFile(refWorktreeLeasePath(p, runID), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	held, lerr := flockTry(file)
	if lerr != nil || !held {
		return nil, false, errors.Join(lerr, file.Close())
	}
	return file, true, nil
}

// ReleaseRefWorktree drops a hold taken by [HoldRefWorktree].
func ReleaseRefWorktree(file *os.File) error {
	if file == nil {
		return nil
	}
	return errors.Join(flockUnlock(file), file.Close())
}

// DispatchContext carries the claim a dispatch runs under, so a write it makes
// after losing that claim is refused rather than applied over the dispatch that
// took it. Every path executing a claimed trigger builds its context here.
func DispatchContext(ctx context.Context, trig *store.Trigger) context.Context {
	return store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{ClaimGeneration: trig.ClaimSeq})
}
