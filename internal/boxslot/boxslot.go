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
// invoked on each wait iteration with the current holder count so
// the caller can surface "waiting for box slot, N ahead" feedback.
// NoWait flips the wait into an immediate [ErrSlotsFull] return -- the
// shape CI runners want when they would rather decline overlap than
// queue.
package boxslot

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
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
	// disables the semaphore (Acquire returns a no-op release).
	MaxSlots int

	// LockDir is the directory that holds the coord + per-holder lock
	// files. Created with 0o700 if missing. Typically [Paths.BoxSlotDir].
	LockDir string

	// NoWait flips the blocking wait into an immediate ErrSlotsFull
	// return when no slot is available at admission time.
	NoWait bool

	// OnWait is called on each wait iteration with the current count
	// of active holders so the caller can surface "waiting, N ahead"
	// feedback. Nil silences the messages.
	OnWait func(activeHolders int)

	// PollInterval is the base wait between admission retries.
	// Jittered up to PollMax. Zero uses sane defaults (1s base, 3s max).
	PollInterval time.Duration
	PollMax      time.Duration
}

// DefaultMaxSlots returns 0 -- the box-slot semaphore is disabled by
// default and opt-in via --sw-box-slots or SPARKWING_BOX_SLOTS. Most
// runs aren't CPU-pegged (Docker pulls, network I/O dominate) and the
// old SQLite-serialization rationale for a default cap is gone in S3
// state mode. Users on a small box who launch concurrent
// CPU-saturating pipelines should set SPARKWING_BOX_SLOTS=N
// explicitly; e.g. `export SPARKWING_BOX_SLOTS=$(( $(sysctl -n hw.ncpu) / 4 ))`
// matches the old conservative heuristic. workersPerRun is retained
// in the signature so callers don't need to change; it's ignored.
func DefaultMaxSlots(workersPerRun int) int { //nolint:revive // arg retained for caller compat
	_ = workersPerRun
	return 0
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
	if opts.MaxSlots <= 0 {
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

	for {
		release, active, err := tryAcquire(opts.LockDir, opts.MaxSlots)
		if err != nil {
			return nil, err
		}
		if release != nil {
			return release, nil
		}
		if opts.NoWait {
			return nil, ErrSlotsFull
		}
		if opts.OnWait != nil {
			opts.OnWait(active)
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
// might drop into the dir.
const holderPrefix = "holder-"

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
	name := fmt.Sprintf("%spid%d-%d-%d.lock",
		holderPrefix,
		os.Getpid(),
		time.Now().UnixNano(),
		holderCounter.Add(1),
	)
	path := filepath.Join(lockDir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("boxslot: create holder %s: %w", path, err)
	}
	if err := flockExclusiveNonblock(f); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("boxslot: lock fresh holder %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(f, "pid=%d start=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	return f, nil
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
