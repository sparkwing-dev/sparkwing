package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

// acquireBoxSlot reserves a host-local concurrency slot before the
// local-run path proceeds. The semaphore caps simultaneous
// `sparkwing run` orchestrator processes on this machine so two
// overlapping invocations don't oversubscribe the box's CPU. It is
// independent of the state backend (Mode 1 through 4 all use it
// uniformly) and independent of any `.Concurrency(...)` per-pipeline
// declarations, which are a separate logical-reservation concern.
//
// Reads:
//
//   - SPARKWING_BOX_SLOTS    Override the slot count. Zero / negative /
//     "off" disables the semaphore entirely.
//   - SPARKWING_BOX_NO_WAIT  Fail with ErrSlotsFull instead of queuing
//     when all slots are taken. CI runners use this to decline overlap.
//
// Default heuristic: max(1, NumCPU / workersHint). workersHint is the
// per-run dispatcher worker cap (Options.MaxParallel), so admitted
// runs sum to approximately NumCPU worker goroutines.
//
// Cluster paths (handle-trigger, run-node, replay-node) do not call
// this -- pod CPU limits and the warm-runner pool's own concurrency
// budget cover that surface.
func acquireBoxSlot(ctx context.Context, paths Paths, workersHint int) (func(), error) {
	maxSlots, ok := parseBoxSlots(os.Getenv("SPARKWING_BOX_SLOTS"))
	if !ok {
		maxSlots = boxslot.DefaultMaxSlots(workersHint)
	}
	if maxSlots <= 0 {
		return func() {}, nil
	}
	noWait := envTruthy("SPARKWING_BOX_NO_WAIT")

	release, err := boxslot.Acquire(ctx, boxslot.Options{
		MaxSlots: maxSlots,
		LockDir:  paths.BoxSlotDir(),
		NoWait:   noWait,
		OnWait: func(active int) {
			fmt.Fprintf(os.Stderr,
				"waiting for box slot (%d active, max %d). "+
					"Override with --sw-box-slots N or fail fast with --sw-no-wait.\n",
				active, maxSlots)
		},
	})
	if err != nil {
		if errors.Is(err, boxslot.ErrSlotsFull) {
			return nil, fmt.Errorf(
				"box slots full (max=%d); pass --sw-box-slots N to raise, "+
					"or drop --sw-no-wait to queue instead",
				maxSlots)
		}
		return nil, fmt.Errorf("acquire box slot: %w", err)
	}
	return release, nil
}

// parseBoxSlots accepts "off" / "none" / "0" / negative as disabled,
// and any positive integer as an explicit cap. Returns (0, false)
// when the env var is unset so callers fall back to the default
// heuristic.
func parseBoxSlots(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	switch s {
	case "off", "none", "disable", "disabled":
		return -1, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

func envTruthy(name string) bool {
	switch os.Getenv(name) {
	case "1", "true", "TRUE", "yes", "YES":
		return true
	}
	return false
}

// workersHintForBoxSlot reports the per-run dispatcher worker cap the
// box-slot heuristic should size against. Mirrors the precedence the
// `<pipeline>` branch will apply when populating Options.MaxParallel.
func workersHintForBoxSlot() int {
	if w := os.Getenv("SPARKWING_WORKERS"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			return n
		}
	}
	return runtime.NumCPU()
}
