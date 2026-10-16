package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// NamedRefWorktreeRepoKey names the trigger environment entry holding the git
// directory a --sw-ref-worktree run's worktree belongs to.
const NamedRefWorktreeRepoKey = "_SPARKWING_SUBMIT_NAMED_WORKTREE_REPO"

const namedRefWorktreePoll = 250 * time.Millisecond

var namedRefWorktreeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// NamedRefWorktree is a worktree that runs reuse, checking their commits out
// into it one at a time.
type NamedRefWorktree struct {
	Dir string
	// Repo is the git common directory Dir is a worktree of.
	Repo string
}

// ResolveNamedRefWorktree names the worktree called name for the repository
// originRepo belongs to. Checkouts of one repository share it.
func ResolveNamedRefWorktree(ctx context.Context, p Paths, originRepo, name string) (NamedRefWorktree, error) {
	if !namedRefWorktreeRE.MatchString(name) {
		return NamedRefWorktree{}, fmt.Errorf(
			"worktree name %q must be 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", name)
	}
	out, err := exec.CommandContext(ctx, "git", "-C", originRepo,
		"rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return NamedRefWorktree{}, fmt.Errorf("%s is not inside a git repository: %w", originRepo, err)
	}
	common := resolvedPath(strings.TrimSpace(string(out)))
	stem := filepath.Base(repoShortName(originRepo))
	if !namedRefWorktreeRE.MatchString(stem) {
		stem = "repo"
	}
	sum := sha256.Sum256([]byte(common))
	hash := hex.EncodeToString(sum[:6])
	return NamedRefWorktree{
		Dir:  filepath.Join(p.NamedRefWorktreesDir(), stem[:min(len(stem), 63-len(hash))]+"-"+hash, name),
		Repo: common,
	}, nil
}

func resolvedPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func waitNamedRefWorktree(ctx context.Context, w NamedRefWorktree, logger *slog.Logger) (*os.File, error) {
	ticker := time.NewTicker(namedRefWorktreePoll)
	defer ticker.Stop()
	for waiting := false; ; waiting = true {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("wait for worktree %s: %w", w.Dir, err)
		}
		file, held, err := tryHoldNamedRefWorktree(w)
		if err != nil || held {
			return file, err
		}
		if !waiting && logger != nil {
			logger.Info("waiting for a run to release named worktree", "dir", w.Dir)
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func tryHoldNamedRefWorktree(w NamedRefWorktree) (*os.File, bool, error) {
	return tryHoldLease(filepath.Dir(w.Dir), w.Dir+refWorktreeLeaseSuffix)
}

// safety: the caller holds the worktree. A worktree of the repository switches
// in place so unchanged files keep their modification times; anything else at
// the directory is replaced.
func checkoutNamedRefWorktree(ctx context.Context, w NamedRefWorktree, commit Commit, logger *slog.Logger) error {
	err := errors.New("not a worktree of " + w.Repo)
	if gitDir, ok := namedRefWorktreeGitDir(ctx, w); ok {
		err = switchNamedRefWorktree(ctx, w.Dir, gitDir, commit)
	}
	if err != nil {
		// safety: a switch a cancel interrupted is not a broken worktree; clearing
		// it would throw away the build output the next run reuses.
		if cerr := ctx.Err(); cerr != nil {
			return errors.Join(err, cerr)
		}
		if logger != nil {
			logger.Info("creating named worktree", "dir", w.Dir, "error", err)
		}
		if err := recreateNamedRefWorktree(ctx, w, commit, logger); err != nil {
			return err
		}
	}
	return requirePipelineDir(w.Dir, commit)
}

// safety: the forced checkout and clean act wherever git resolves the
// directory, so it must be its own top level and the worktree the repository
// registered at this very path, not a symlink, an enclosing checkout or a .git
// file aimed at another worktree.
func namedRefWorktreeGitDir(ctx context.Context, w NamedRefWorktree) (string, bool) {
	out, err := exec.CommandContext(ctx, "git", "-C", w.Dir, "rev-parse", "--path-format=absolute",
		"--show-toplevel", "--absolute-git-dir", "--git-common-dir").Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return "", false
	}
	top, gitDir, common := resolvedPath(lines[0]), resolvedPath(lines[1]), resolvedPath(lines[2])
	registered, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return "", false
	}
	dir := filepath.Join(resolvedPath(filepath.Dir(w.Dir)), filepath.Base(w.Dir))
	return gitDir, top == dir && common == w.Repo &&
		filepath.Dir(gitDir) == filepath.Join(common, "worktrees") &&
		resolvedPath(strings.TrimSpace(string(registered))) == filepath.Join(dir, ".git")
}

func switchNamedRefWorktree(ctx context.Context, dir, gitDir string, commit Commit) error {
	// bug: the run's processes do not hold the lease, so one that outlives a
	// consumer killed abruptly can still be running git here; a cancel stops
	// them, and then the held lease means only a stale index.lock is left.
	if err := os.Remove(filepath.Join(gitDir, "index.lock")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale index lock: %w", err)
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", dir,
		"checkout", "--detach", "--force", "--quiet", string(commit), "--").CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout %s: %w: %s", commit, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", dir,
		"clean", "-d", "-ff", "--quiet").CombinedOutput(); err != nil {
		return fmt.Errorf("git clean: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// safety: these ignored paths decide what the pipeline compiles, so one an
	// earlier run left would build into this run without the gate seeing it.
	if out, err := exec.CommandContext(ctx, "git", "-C", dir,
		"clean", "-d", "-ffX", "--quiet", "--", ".sparkwing", "go.work", "go.work.sum").CombinedOutput(); err != nil {
		return fmt.Errorf("git clean pipeline inputs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func recreateNamedRefWorktree(ctx context.Context, w NamedRefWorktree, commit Commit, logger *slog.Logger) error {
	if err := os.RemoveAll(w.Dir); err != nil {
		return fmt.Errorf("clear named worktree %s: %w", w.Dir, err)
	}
	// safety: the registration of a directory removed behind git's back makes
	// `worktree add` refuse the path.
	out, err := exec.CommandContext(ctx, "git", "-C", w.Repo, "worktree", "prune").CombinedOutput()
	logBestEffortGit(ctx, logger, slog.LevelWarn, "worktree prune", out, err)
	return addRefWorktree(ctx, w.Repo, w.Dir, commit)
}

// safety: the worktree is cleared and recreated in place, so a symlinked
// component would carry that removal outside the root.
func withinNamedRefWorktrees(p Paths, dir string) bool {
	root := filepath.Clean(p.NamedRefWorktreesDir())
	rel, err := filepath.Rel(root, filepath.Clean(dir))
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || !namedRefWorktreeRE.MatchString(parts[0]) || !namedRefWorktreeRE.MatchString(parts[1]) {
		return false
	}
	for _, path := range []string{filepath.Join(root, parts[0]), filepath.Join(root, parts[0], parts[1])} {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func sweepNamedRefWorktrees(p Paths, logger *slog.Logger) (int, error) {
	root := p.NamedRefWorktreesDir()
	repos, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read named worktree directory: %w", err)
	}
	removed := 0
	var failures []error
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}
		names, err := os.ReadDir(filepath.Join(root, repo.Name()))
		if err != nil {
			failures = append(failures, fmt.Errorf("named worktree directory %s: %w", repo.Name(), err))
			continue
		}
		for _, name := range names {
			dir := filepath.Join(root, repo.Name(), name.Name())
			if namedRefWorktreeRepoExists(dir) {
				continue
			}
			done, err := reclaimNamedRefWorktree(dir)
			if err != nil {
				failures = append(failures, fmt.Errorf("named worktree %s: %w", dir, err))
				continue
			}
			if done {
				removed++
				if logger != nil {
					logger.Info("removed a named worktree whose repository is gone", "dir", dir)
				}
			}
		}
	}
	return removed, errors.Join(failures...)
}

func namedRefWorktreeRepoExists(dir string) bool {
	gitDir := gitDirPointer(filepath.Join(dir, ".git"), dir)
	if gitDir == "" {
		// safety: a worktree whose pointer is unreadable mid-creation is
		// treated as live; only a pointer to a missing directory is proof.
		return true
	}
	_, err := os.Stat(gitDir)
	return err == nil
}

// safety: the lease file stays, because unlinking it while held would let a
// waiter lock the old file while a newcomer locks a new one.
func reclaimNamedRefWorktree(dir string) (bool, error) {
	file, held, err := tryHoldNamedRefWorktree(NamedRefWorktree{Dir: dir})
	if err != nil || !held {
		return false, err
	}
	if namedRefWorktreeRepoExists(dir) {
		return false, ReleaseRefWorktree(file)
	}
	return true, errors.Join(os.RemoveAll(dir), ReleaseRefWorktree(file))
}
