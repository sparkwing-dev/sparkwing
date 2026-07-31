package sparkwing

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// lintSlotRoot is the directory under toolCacheRoot that parents every
// lint slot.
const lintSlotRoot = "lintslots"

// defaultLintSlots is how many canonical paths a tool gets. The number
// trades reuse against fallbacks: a run that finds every slot busy
// falls back to a private cache and pays a cold start, while every
// extra slot is one more cache that has to warm up and stay on disk.
//
// Four is deliberate rather than tuned. golangci-lint takes a
// box-wide lock, so with --allow-serial-runners the effective number
// of lints running at once on a machine is one and even two slots
// would rarely fall back. Four leaves room for repos that pass
// --allow-parallel-runners without letting the slot set grow to the
// point where slots stop being reused.
const defaultLintSlots = 4

// LintSlotsEnv overrides how many slots a tool gets. A value below one,
// or one that does not parse, leaves defaultLintSlots in place.
const LintSlotsEnv = "SPARKWING_LINT_SLOTS"

// LintSlot is a lease on a canonical path to lint through, held until
// [LintSlot.Release].
//
// Path is the directory to run the tool in and Cache is the cache
// directory to hand it. Both are stable across leases, which is the
// whole point: see [AcquireLintSlot].
type LintSlot struct {
	// Path is the directory the leased tree is visible at. Run the
	// linter here rather than in [WorkDir].
	Path string

	// Cache is the tool cache directory that goes with Path.
	Cache string

	// Canonical reports whether Path is a shared slot. It is false
	// when the lease fell back to a private per-worktree cache,
	// which is correct but starts cold.
	Canonical bool

	lock *os.File
}

// AcquireLintSlot leases a canonical path for one lint run, so that a
// cache can be reused between worktrees without misreporting where a
// finding lives.
//
// The problem it solves. golangci-lint keys a cached result on the
// package's import path and its files' content, with the main module's
// absolute directory deliberately stripped, so two worktrees of one
// repo holding the same bytes land on the same cache entry. The stored
// issue, though, carries the absolute filename of the tree that
// produced it. Share one cache directory between worktrees and every
// replayed finding names the other worktree's files -- measured on
// sparkwing at 49 findings out of 49 -- and once that worktree is
// deleted the run names files nobody can open. [ToolCacheDir] gives
// each worktree its own directory to stop exactly that, which is
// correct and costs a cold start per worktree: 95.86s against 2.22s
// warm on a 205k-line module.
//
// What a slot changes. A slot is a fixed absolute path -- a symlink
// repointed at whichever worktree currently holds the lease -- plus
// the cache that goes with it. Every run through slot i sees the same
// absolute path, so a replayed issue's stored filename resolves under
// the current holder. The lease is exclusive, so "the current holder"
// is never ambiguous, and two worktrees can lint at once through
// different slots and each report only its own. The path is an alias
// rather than a fiction: it opens the holder's real file, and the
// linter's default output stays relative to the directory it ran in,
// so it reads exactly as it does today.
//
// What a slot does not change. Nothing here can hide a finding.
// Content is part of the cache key, so a file that differs from the
// one that filled the cache is a miss and gets analyzed. A slot makes
// a cold run warm; it never makes a run look at less.
//
// Call Release when the run is done, from a defer -- the lock is also
// dropped by the OS if the process dies, so a crash frees the slot.
// Point the command at the slot with [LintSlot.Configure] rather than
// by hand, because a plain working directory is not enough; see there.
//
//	slot, err := sparkwing.AcquireLintSlot("golangci-lint")
//	if err != nil {
//		return err
//	}
//	defer slot.Release()
//
//	cmd := sparkwing.Bash(ctx, "golangci-lint run --allow-serial-runners ./...")
//	_, err = slot.Configure(cmd, "GOLANGCI_LINT_CACHE").Run()
//
// A lease always succeeds. When every slot is busy, or the platform
// will not give an unprivileged process a symlink, the returned slot
// is the worktree's own path and its private [ToolCacheDir]: cold, and
// no less correct than not calling this at all. Canonical says which
// of the two was handed back.
func AcquireLintSlot(tool string) (*LintSlot, error) {
	fallback := &LintSlot{Path: lintScope(), Cache: ToolCacheDir(tool)}

	scope := lintScope()
	if scope == "" {
		return fallback, nil
	}

	root := filepath.Join(os.TempDir(), toolCacheRoot, lintSlotRoot, cacheSegment(tool, "tool"))
	for i := range lintSlotCount() {
		slot, err := claimLintSlot(root, i, scope)
		if errors.Is(err, errLintSlotBusy) {
			continue
		}
		if err != nil {
			return fallback, nil
		}
		return slot, nil
	}
	return fallback, nil
}

// errLintSlotBusy means another process holds this slot. It is an
// ordinary outcome, not a failure: the caller tries the next slot.
var errLintSlotBusy = errors.New("lint slot busy")

// claimLintSlot takes slot i under root for the tree at scope. It
// creates the slot's directories, takes the exclusive lock without
// waiting, and only then repoints the slot's tree symlink -- holding
// the lock is what makes replacing the symlink safe.
func claimLintSlot(root string, i int, scope string) (*LintSlot, error) {
	dir := filepath.Join(root, "slot-"+strconv.Itoa(i))
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return nil, err
	}

	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flockExclusiveNonblock(lock); err != nil {
		_ = lock.Close()
		return nil, errLintSlotBusy
	}

	tree := filepath.Join(dir, "tree")
	if err := os.Remove(tree); err != nil && !errors.Is(err, os.ErrNotExist) {
		releaseLintLock(lock)
		return nil, err
	}
	if err := os.Symlink(scope, tree); err != nil {
		releaseLintLock(lock)
		return nil, err
	}

	return &LintSlot{Path: tree, Cache: cache, Canonical: true, lock: lock}, nil
}

// Configure points c at the slot: the working directory, PWD and the
// tool's cache variable, set together because setting only some of
// them is silently wrong.
//
// PWD is the part that is easy to leave out and expensive to leave
// out. Go's os.Getwd prefers $PWD whenever it names the same directory
// as ".", and a bare change of directory into a symlink leaves PWD
// alone, so the linter resolves the slot and sees the worktree's own
// path instead. It then stores that path in the shared cache and the
// next holder is back to being told about a tree it is not in.
// Measured on golangci-lint 2.12.2: chdir alone reports
// <worktree>/probe.go, chdir with PWD reports <slot>/tree/probe.go.
//
// cacheVar is the tool's own cache environment variable, such as
// GOLANGCI_LINT_CACHE.
func (s *LintSlot) Configure(c *Cmd, cacheVar string) *Cmd {
	return c.Dir(s.Path).Env("PWD", s.Path).Env(cacheVar, s.Cache)
}

// Release drops the lease. It is safe to call on a fallback slot and
// safe to call more than once. The slot's cache is deliberately left
// behind -- it is what the next holder inherits -- while the tree
// symlink is removed so an idle slot does not point at a worktree that
// may since have been deleted.
func (s *LintSlot) Release() {
	if s == nil || s.lock == nil {
		return
	}
	if s.Canonical {
		_ = os.Remove(s.Path)
	}
	releaseLintLock(s.lock)
	s.lock = nil
}

// releaseLintLock unlocks and closes a slot lock file. Closing alone
// would drop the flock, but unlocking first says so.
func releaseLintLock(f *os.File) {
	_ = flockUnlock(f)
	_ = f.Close()
}

// lintSlotCount is how many slots to try, from LintSlotsEnv or the
// default.
func lintSlotCount() int {
	if n, err := strconv.Atoi(os.Getenv(LintSlotsEnv)); err == nil && n > 0 {
		return n
	}
	return defaultLintSlots
}

// lintScope is the absolute directory a lease is for: the pipeline's
// work directory, or the process's own when there is none.
func lintScope() string {
	if dir := WorkDir(); dir != "" {
		return dir
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}
