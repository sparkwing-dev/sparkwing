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
// Cap precedence (highest first):
//
//   - SPARKWING_BOX_SLOTS_PIN  An explicit per-run --sw-box-slots pin.
//     Fixed for the run; outranks the live host control so a deliberate
//     per-run choice is honored even while the host cap is retuned.
//   - the live host control       The value box-slots set writes, re-read
//     on every acquire poll so a waiting or holding run retunes in place.
//   - SPARKWING_BOX_SLOTS         Ambient env baseline.
//   - the heuristic default       max(1, NumCPU / workersHint).
//
// "off" / "none" / "0" / negative at any level disables the semaphore.
// SPARKWING_BOX_NO_WAIT makes a full host fail with ErrSlotsFull instead
// of queuing -- CI runners use it to decline overlap. Unless the run is
// pinned, the cap is resolved fresh on each poll, so box-slots set takes
// effect on the next wait iteration of every queued run.
//
// Cluster paths (handle-trigger, run-node, replay-node) do not call
// this -- pod CPU limits and the warm-runner pool's own concurrency
// budget cover that surface.
func acquireBoxSlot(ctx context.Context, paths Paths, workersHint int) (func(), error) {
	noWait := envTruthy("SPARKWING_BOX_NO_WAIT")
	autoReap := !envTruthy("SPARKWING_BOX_NO_AUTOREAP")

	resolve := func() int { cap, _ := BoxSlotCap(paths, workersHint); return cap }
	if pin, ok := parseBoxSlots(os.Getenv("SPARKWING_BOX_SLOTS_PIN")); ok {
		resolve = func() int { return pin }
	}

	stallTTL, err := boxslot.StallTTL()
	if err != nil {
		return nil, err
	}

	release, err := boxslot.Acquire(ctx, boxslot.Options{
		ResolveMaxSlots: resolve,
		LockDir:         paths.BoxSlotDir(),
		NoWait:          noWait,
		OnWait: func(active, max int) {
			fmt.Fprintf(os.Stderr,
				"waiting for box slot (%d active, max %d). "+
					"Raise the host cap live with `sparkwing box-slots set --to N`, "+
					"or fail fast with --sw-no-wait.\n",
				active, max)
		},
		RunsDir:  paths.RunsDir(),
		StallTTL: stallTTL,
		AutoReap: autoReap,
		OnStalled: func(stalled []boxslot.StalledHolder) {
			for _, s := range stalled {
				if autoReap {
					fmt.Fprintf(os.Stderr,
						"box slot held by a stalled process: pid %d (%s); reaping it to free the slot "+
							"(set SPARKWING_BOX_NO_AUTOREAP=1 to only warn).\n",
						s.PID, s.Evidence)
					continue
				}
				fmt.Fprintf(os.Stderr,
					"box slot held by a stalled process: pid %d (%s). "+
						"Inspect with `sparkwing box-slots sweep`; clear with "+
						"`sparkwing box-slots sweep --reap`.\n",
					s.PID, s.Evidence)
			}
		},
	})
	if err != nil {
		if errors.Is(err, boxslot.ErrSlotsFull) {
			return nil, fmt.Errorf(
				"box slots full; raise the host cap with `sparkwing box-slots set --to N`, " +
					"or drop --sw-no-wait to queue instead")
		}
		return nil, fmt.Errorf("acquire box slot: %w", err)
	}
	return release, nil
}

// annotateBoxSlotHolder ties this process's box-slot holder marker to
// the run it admitted, so `box-slots list` can name the run behind a
// wedged holder by reading the marker file. Best-effort: the run
// proceeds identically when no marker exists (semaphore disabled,
// cluster path) or the append fails -- failure only costs diagnostics.
func annotateBoxSlotHolder(paths Paths, runID string) {
	_ = boxslot.AnnotateHolder(paths.BoxSlotDir(), runID)
}

// BoxSlotCap reports the host box-slot cap a new run with no explicit
// --sw-box-slots pin would resolve to, and where it came from
// ("control", "env", or "default"). It is the host-level view
// box-slots show renders and the resolver acquireBoxSlot re-reads on
// each poll; per-run pins are deliberately not consulted here.
func BoxSlotCap(paths Paths, workersHint int) (slots int, source string) {
	if v, ok, err := boxslot.ReadControl(paths.BoxSlotDir()); err == nil && ok {
		if n, parsed := parseBoxSlots(v); parsed {
			return n, "control"
		}
	}
	if n, ok := parseBoxSlots(os.Getenv("SPARKWING_BOX_SLOTS")); ok {
		return n, "env"
	}
	return boxslot.DefaultMaxSlots(workersHint), "default"
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

func boxSlotPinned() bool {
	_, ok := parseBoxSlots(os.Getenv("SPARKWING_BOX_SLOTS_PIN"))
	return ok
}

// HostBoxSlotCap is BoxSlotCap with the standard workers hint applied,
// for callers (the box-slots CLI) that want the host-level view without
// re-deriving the heuristic input.
func HostBoxSlotCap(paths Paths) (slots int, source string) {
	return BoxSlotCap(paths, workersHintForBoxSlot())
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
