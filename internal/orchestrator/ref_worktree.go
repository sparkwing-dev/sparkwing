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
// --sw-ref submission resolved to. Deduplication compares it, so a repeat key
// naming a different tree is refused rather than answered with the first run.
const RefWorktreeRevKey = "_SPARKWING_SUBMIT_REF_REV"

const refWorktreeAbsentRunGrace = 10 * time.Minute

const refWorktreeGitTimeout = 10 * time.Second

// CreateRefWorktree checks rev out into a worktree owned by runID. Nothing here
// removes it: the tree outlives this process so a detached run can execute it.
// Callers pass a commit from [ResolveRefCommit], never a bare ref name.
func CreateRefWorktree(
	ctx context.Context, p Paths, originRepo, rev, runID string, logger *slog.Logger,
) (string, error) {
	if err := fssecure.EnsureDir(p.RefWorktreesDir()); err != nil {
		return "", fmt.Errorf("secure ref worktree directory: %w", err)
	}
	dir := p.RefWorktreeDir(runID)
	// safety: removal refuses a path outside the root, so a run id that escapes
	// it would build a tree nothing can ever reclaim.
	if !withinRefWorktrees(p, dir) {
		return "", fmt.Errorf("run id %q does not name a directory inside %s", runID, p.RefWorktreesDir())
	}
	out, err := exec.CommandContext(ctx, "git", "-C", originRepo,
		"worktree", "add", "--detach", "--quiet", "--", dir, rev).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add %s: %w: %s", rev, err, strings.TrimSpace(string(out)))
	}
	if info, serr := os.Stat(filepath.Join(dir, ".sparkwing")); serr != nil || !info.IsDir() {
		if rerr := RemoveRefWorktree(ctx, p, dir, logger); rerr != nil {
			return "", rerr
		}
		return "", fmt.Errorf("commit %s has no .sparkwing/ directory, so no pipeline can be resolved from it", rev)
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

// SweepRefWorktrees reclaims worktrees whose runs have ended, or whose
// submissions never produced one, and reports how many it removed. It recovers
// a worktree whose consumer died before it could clean up.
func SweepRefWorktrees(ctx context.Context, p Paths, st *store.Store, logger *slog.Logger) (int, error) {
	entries, err := os.ReadDir(p.RefWorktreesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read ref worktree directory: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		reclaimable, rerr := refWorktreeIsReclaimable(ctx, st, entry)
		if rerr != nil {
			return removed, fmt.Errorf("ref worktree %s: %w", entry.Name(), rerr)
		}
		if !reclaimable {
			continue
		}
		if err := RemoveRefWorktree(ctx, p, p.RefWorktreeDir(entry.Name()), logger); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func refWorktreeIsReclaimable(ctx context.Context, st *store.Store, entry os.DirEntry) (bool, error) {
	run, err := st.GetRun(ctx, entry.Name())
	if errors.Is(err, store.ErrNotFound) {
		// safety: a submission builds the worktree before it inserts the run, so
		// a sweep racing one would delete the tree out from under it.
		info, ierr := entry.Info()
		if ierr != nil {
			return false, ierr
		}
		return time.Since(info.ModTime()) > refWorktreeAbsentRunGrace, nil
	}
	if err != nil {
		return false, err
	}
	return isTerminalRunStatus(run.Status), nil
}
