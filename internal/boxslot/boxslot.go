// Package boxslot is a host-local counting semaphore implemented with
// OS file locks. It caps the number of concurrent orchestrator
// processes on a single machine so two overlapping `sparkwing run`
// invocations don't oversubscribe the host's CPU.
//
// The semaphore is intentionally separate from the run/state
// concurrency mechanisms in pkg/store (.Cache(), AcquireSlot, etc.).
// Those primitives express *logical* per-pipeline reservation against
// the shared state backend; boxslot expresses *physical* per-host CPU
// admission with no state-backend involvement. It works identically
// across all four deployment modes.
//
// # Mechanism
//
// Each holder owns a per-process lock file under [Paths.BoxSlotDir]
// named holder-<nonce>.lock. The holder keeps an exclusive flock on
// the file for its lifetime; the OS releases the flock when the
// process exits even on crash or SIGKILL, so stale holder files
// self-heal -- a subsequent acquirer that can successfully flock a
// holder file knows the original owner is gone and deletes it.
//
// Admission decisions are serialized via a single coord.lock file:
// candidates take an exclusive flock on coord.lock, count live
// holders (by attempting non-blocking flocks on the other files),
// and create their own holder file before releasing coord.lock. The
// short critical section caps contention at a few syscalls per
// admission attempt.
//
// # Wait semantics
//
// When all slots are taken and [Options.NoWait] is false (the
// default), Acquire polls with jittered backoff until either a slot
// frees up or the context is canceled. [Options.OnWait], if set, is
// invoked on each wait iteration with the current holder count and the
// cap in force, so the caller can surface "waiting for box slot, N
// active, max M" feedback. NoWait flips the wait into an immediate
// [ErrSlotsFull] return -- the shape CI runners want when they would
// rather decline overlap than queue.
//
// # Live cap
//
// The cap can be a fixed [Options.MaxSlots] or, via
// [Options.ResolveMaxSlots], re-read on every poll so an operator can
// retune host concurrency while runs are queued or holding.
// [WriteControl] / [ReadControl] back a host-wide control value, and
// [Status] reports live holders and waiters for observability.
package boxslot

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// ErrSlotsFull is returned by Acquire when [Options.NoWait] is set
// and no slot was available at the moment of the call.
var ErrSlotsFull = errors.New("box slots full")

// Options controls a single Acquire invocation.
type Options struct {
	// MaxSlots caps concurrent holders on this host. Zero or negative
	// disables the semaphore (Acquire returns a no-op release). Ignored
	// when ResolveMaxSlots is set.
	MaxSlots int

	// ResolveMaxSlots, when non-nil, is consulted on every admission
	// attempt -- including each wait-poll iteration -- to obtain the
	// current cap, so the host concurrency can be retuned while a run
	// is queued or already holding. A resolved value <= 0 disables the
	// semaphore for that attempt and admits immediately. When nil the
	// static MaxSlots is used.
	ResolveMaxSlots func() int

	// LockDir is the directory that holds the coord + per-holder lock
	// files. Created with 0o700 if missing. Typically [Paths.BoxSlotDir].
	LockDir string

	// NoWait flips the blocking wait into an immediate ErrSlotsFull
	// return when no slot is available at admission time.
	NoWait bool

	// OnWait is called on each wait iteration with the current count of
	// active holders and the cap in force that iteration, so the caller
	// can surface "waiting, N active, max M" feedback. Nil silences the
	// messages.
	OnWait func(activeHolders, maxSlots int)

	// RunsDir is the per-run artifacts root the stalled-holder probe
	// reads envelope mtimes from. Required (with OnStalled and a
	// positive StallTTL) for the probe to run.
	RunsDir string

	// StallTTL is the silence threshold the wait-path probe hands to
	// SweepStalled. Zero or negative disables the probe.
	StallTTL time.Duration

	// OnStalled is called at most once per probe interval, while
	// waiting for a slot, with the live holders that look stalled --
	// so a queued run can say who it is stuck behind. Reporting only:
	// the wait path never signals or removes a holder; reaping is the
	// operator's explicit act via ReapStalled / box-slots sweep --reap.
	// Nil disables the probe.
	OnStalled func([]StalledHolder)

	// PollInterval is the base wait between admission retries.
	// Jittered up to PollMax. Zero uses sane defaults (1s base, 3s max).
	PollInterval time.Duration
	PollMax      time.Duration
}

// DefaultMaxSlots returns the default host box-slot cap: max(1,
// NumCPU/workersPerRun). Sizing the cap so the admitted runs sum to
// about NumCPU worker goroutines keeps overlapping `sparkwing run`
// invocations from oversubscribing the host's CPU. On a shared local
// SQLite backend this also prevents the lease-heartbeat dogpile that
// collapses concurrent runs: without a host admission cap, enough
// overlap saturates the single SQLite writer, heartbeats start failing
// with SQLITE_BUSY, leases expire, and holders get superseded in a
// feedback loop. The semaphore stays overridable via --sw-box-slots or
// SPARKWING_BOX_SLOTS ("off"/"0" disables it). workersPerRun is the
// per-run dispatcher worker cap; values below 1 are treated as 1.
func DefaultMaxSlots(workersPerRun int) int {
	if workersPerRun < 1 {
		workersPerRun = 1
	}
	slots := runtime.NumCPU() / workersPerRun
	if slots < 1 {
		slots = 1
	}
	return slots
}

// Acquire blocks until a box slot is available, then returns a
// release function the caller must invoke (typically via defer) to
// free the slot. ctx cancellation aborts the wait cleanly.
//
// When opts.MaxSlots <= 0 the semaphore is disabled and Acquire
// returns immediately with a no-op release. When opts.NoWait is true
// and no slot is available at admission time, Acquire returns
// ErrSlotsFull without waiting.
//
// The returned release is safe to call exactly once. Calling it
// after process exit is unnecessary -- the OS releases the holder's
// flock on exit, and a subsequent acquirer reclaims the slot
// automatically.
func Acquire(ctx context.Context, opts Options) (release func(), err error) {
	resolveMax := opts.ResolveMaxSlots
	if resolveMax == nil {
		fixed := opts.MaxSlots
		resolveMax = func() int { return fixed }
	}
	if resolveMax() <= 0 {
		return func() {}, nil
	}
	if opts.LockDir == "" {
		return nil, errors.New("boxslot: Options.LockDir required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	if opts.PollMax < opts.PollInterval {
		opts.PollMax = 3 * opts.PollInterval
	}
	if err := os.MkdirAll(opts.LockDir, 0o700); err != nil {
		return nil, fmt.Errorf("boxslot: prepare %s: %w", opts.LockDir, err)
	}

	var waiter *os.File
	defer func() {
		if waiter != nil {
			_ = os.Remove(waiter.Name())
			_ = flockUnlock(waiter)
			_ = waiter.Close()
		}
	}()

	var lastStallProbe time.Time
	for {
		max := resolveMax()
		if max <= 0 {
			return func() {}, nil
		}
		release, active, err := tryAcquire(opts.LockDir, max)
		if err != nil {
			return nil, err
		}
		if release != nil {
			return release, nil
		}
		if opts.NoWait {
			return nil, ErrSlotsFull
		}
		if waiter == nil {
			// hack: ignore the error; a missing waiter marker only costs box-slots queue visibility.
			waiter, _ = createLockFile(opts.LockDir, waiterPrefix)
		}
		if opts.OnWait != nil {
			opts.OnWait(active, max)
		}
		if opts.OnStalled != nil && opts.RunsDir != "" && opts.StallTTL > 0 &&
			time.Since(lastStallProbe) >= stallProbeInterval {
			lastStallProbe = time.Now()
			if stalled, err := SweepStalled(opts.LockDir, opts.RunsDir, opts.StallTTL); err == nil && len(stalled) > 0 {
				opts.OnStalled(stalled)
			}
		}
		wait := opts.PollInterval + time.Duration(rand.Int64N(int64(opts.PollInterval)))
		if wait > opts.PollMax {
			wait = opts.PollMax
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// tryAcquire makes one admission attempt. Returns (release, _, nil)
// on success, (nil, activeHolders, nil) on slots-full, or
// (nil, 0, err) on hard errors.
func tryAcquire(lockDir string, max int) (func(), int, error) {
	coord, err := openCoord(lockDir)
	if err != nil {
		return nil, 0, err
	}
	defer coord.Close()
	if err := flockExclusive(coord); err != nil {
		return nil, 0, fmt.Errorf("boxslot: coord flock: %w", err)
	}
	defer func() { _ = flockUnlock(coord) }()

	active, err := countActiveHolders(lockDir)
	if err != nil {
		return nil, 0, err
	}
	removeStale(lockDir, waiterPrefix)
	if active >= max {
		return nil, active, nil
	}
	holder, err := createHolder(lockDir)
	if err != nil {
		return nil, 0, err
	}
	return makeRelease(holder), active + 1, nil
}

// holderPrefix scopes the per-process lock files so countActiveHolders
// doesn't trip on coord.lock or any other unrelated file an operator
// might drop into the dir. waiterPrefix scopes the queue markers that
// make box-slots show able to report how many runs are blocked waiting.
const (
	holderPrefix = "holder-"
	waiterPrefix = "waiter-"
)

// controlFileName holds the live host cap an operator can retune with
// box-slots set; the acquire poll re-reads it through ResolveMaxSlots.
const controlFileName = "cap.control"

func openCoord(lockDir string) (*os.File, error) {
	return os.OpenFile(filepath.Join(lockDir, "coord.lock"),
		os.O_CREATE|os.O_RDWR, 0o600)
}

// countActiveHolders scans the holder-*.lock files. For each, it
// attempts a non-blocking exclusive flock: success means the original
// holder is gone (the kernel released its flock on process exit), so
// the stale file is removed and doesn't count. Failure means another
// process still holds the slot, so it counts toward the active total.
func countActiveHolders(lockDir string) (int, error) {
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		return 0, err
	}
	active := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), holderPrefix) {
			continue
		}
		path := filepath.Join(lockDir, e.Name())
		f, err := os.OpenFile(path, os.O_RDWR, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return 0, err
		}
		if err := flockExclusiveNonblock(f); err != nil {
			_ = f.Close()
			active++
			continue
		}
		_ = os.Remove(path)
		_ = flockUnlock(f)
		_ = f.Close()
	}
	return active, nil
}

var holderCounter atomic.Uint64

func createHolder(lockDir string) (*os.File, error) {
	return createLockFile(lockDir, holderPrefix)
}

// createLockFile creates a flock-held marker file named prefix-<unique>
// and keeps it locked for the caller's lifetime. Holders and waiters
// share the mechanism: the kernel releases the flock on process exit,
// so a crashed owner's marker is reclaimable by removeStale.
func createLockFile(lockDir, prefix string) (*os.File, error) {
	name := fmt.Sprintf("%spid%d-%d-%d.lock",
		prefix,
		os.Getpid(),
		time.Now().UnixNano(),
		holderCounter.Add(1),
	)
	path := filepath.Join(lockDir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("boxslot: create %s: %w", path, err)
	}
	if err := flockExclusiveNonblock(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("boxslot: lock fresh %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(f, "pid=%d start=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	return f, nil
}

// removeStale flocks each prefix-matched marker non-blockingly; success
// means the owner is gone, so the marker is deleted. Holders are GC'd
// inside countActiveHolders under coord.lock; waiter markers have no
// admission role, so admission sweeps them opportunistically.
func removeStale(lockDir, prefix string) {
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		path := filepath.Join(lockDir, e.Name())
		f, err := os.OpenFile(path, os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		if err := flockExclusiveNonblock(f); err != nil {
			_ = f.Close()
			continue
		}
		_ = os.Remove(path)
		_ = flockUnlock(f)
		_ = f.Close()
	}
}

// countLocked counts prefix-matched markers whose owner is still alive
// (a non-blocking flock fails), without removing anything. Read-only so
// Status never mutates the lock dir as a side effect of observing it.
func countLocked(lockDir, prefix string) (int, error) {
	entries, err := os.ReadDir(lockDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		path := filepath.Join(lockDir, e.Name())
		f, err := os.OpenFile(path, os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		if err := flockExclusiveNonblock(f); err != nil {
			_ = f.Close()
			n++
			continue
		}
		_ = flockUnlock(f)
		_ = f.Close()
	}
	return n, nil
}

// Stat is the observable state of the host semaphore: how many runs
// currently hold a slot and how many are blocked waiting for one.
type Stat struct {
	ActiveHolders int
	Waiters       int
}

// Status reports the live holder and waiter counts in lockDir without
// mutating it. An absent lock dir reports a zero Stat (no run has ever
// touched the semaphore on this host).
func Status(lockDir string) (Stat, error) {
	if lockDir == "" {
		return Stat{}, errors.New("boxslot: lockDir required")
	}
	if _, err := os.Stat(lockDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Stat{}, nil
		}
		return Stat{}, err
	}
	holders, err := countLocked(lockDir, holderPrefix)
	if err != nil {
		return Stat{}, err
	}
	waiters, err := countLocked(lockDir, waiterPrefix)
	if err != nil {
		return Stat{}, err
	}
	return Stat{ActiveHolders: holders, Waiters: waiters}, nil
}

// ReadControl returns the live host cap an operator set with WriteControl.
// ok is false when no control has been written (absent or blank file), so
// callers fall through to their next precedence source.
func ReadControl(lockDir string) (value string, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(lockDir, controlFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", false, nil
	}
	return v, true, nil
}

// WriteControl persists value as the live host cap, replacing any prior
// control. The write is atomic (temp file + rename) so a concurrent
// ReadControl never observes a half-written value.
func WriteControl(lockDir, value string) error {
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return fmt.Errorf("boxslot: prepare %s: %w", lockDir, err)
	}
	tmp, err := os.CreateTemp(lockDir, controlFileName+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := fmt.Fprintln(tmp, value); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(lockDir, controlFileName)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// ClearControl removes the live cap control so resolution falls back to
// the environment / heuristic default. A no-op when none is set.
func ClearControl(lockDir string) error {
	err := os.Remove(filepath.Join(lockDir, controlFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func makeRelease(holder *os.File) func() {
	var once atomic.Bool
	path := holder.Name()
	return func() {
		if !once.CompareAndSwap(false, true) {
			return
		}
		_ = os.Remove(path)
		_ = flockUnlock(holder)
		_ = holder.Close()
	}
}
