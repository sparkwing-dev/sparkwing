package orchestrator

import (
	"os"
	"runtime"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

// BoxSlotCap reports the host box-slot cap and where it came from
// ("control", "env", or "default"). Box slots no longer gate the run
// path -- the local admission daemon owns host admission -- but the
// box-slots CLI still renders the host-level view until that surface is
// retired.
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

// HostBoxSlotCap is BoxSlotCap with the standard workers hint applied,
// for callers (the box-slots CLI) that want the host-level view without
// re-deriving the heuristic input.
func HostBoxSlotCap(paths Paths) (slots int, source string) {
	return BoxSlotCap(paths, workersHintForBoxSlot())
}

// workersHintForBoxSlot reports the per-run dispatcher worker cap the
// box-slot heuristic sizes against.
func workersHintForBoxSlot() int {
	if w := os.Getenv("SPARKWING_WORKERS"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 {
			return n
		}
	}
	return runtime.NumCPU()
}
