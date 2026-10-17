package runspec

import (
	"errors"
	"math"
	"strings"
	"time"
)

// PlacementStep gives one claimant class a hold before later preferences.
type PlacementStep struct {
	Target string        `json:"target"`
	Hold   time.Duration `json:"hold"`
}

// BurstPlacement seals ordered claimant preferences and an optional monthly overflow budget.
type BurstPlacement struct {
	Preferences     []PlacementStep `json:"preferences"`
	OverflowMinutes *int64          `json:"overflow_minutes,omitempty"`
}

// Validate refuses ambiguous placement steps and invalid overflow budgets.
func (b BurstPlacement) Validate() error {
	if len(b.Preferences) == 0 {
		return errors.New("burst placement requires preferences")
	}
	seen := map[string]bool{}
	var total time.Duration
	for _, step := range b.Preferences {
		selector := strings.HasPrefix(step.Target, "name=") || strings.HasPrefix(step.Target, "pool=")
		if step.Target != "here" && step.Target != "machines" && step.Target != "cloud" && !selector {
			return errors.New("invalid burst placement target")
		}
		if selector && (strings.TrimSpace(strings.SplitN(step.Target, "=", 2)[1]) == "" || strings.ContainsAny(step.Target, ", \t\r\n")) {
			return errors.New("invalid burst placement selector")
		}
		if seen[step.Target] || step.Hold < 0 || step.Hold > time.Duration(math.MaxInt64)-total {
			return errors.New("invalid burst placement hold or duplicate target")
		}
		seen[step.Target] = true
		total += step.Hold
	}
	if b.OverflowMinutes != nil && (*b.OverflowMinutes < 0 || *b.OverflowMinutes > math.MaxInt64/60) {
		return errors.New("invalid overflow minutes")
	}
	return nil
}
