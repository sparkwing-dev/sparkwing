package settings

import (
	"fmt"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"math"
)

// WarmCapacitySettings controls the idle nodes held for one hardware class.
type WarmCapacitySettings struct {
	Class                  string  `json:"class"`
	Mode                   string  `json:"mode"`
	ManualCount            int64   `json:"manual_count"`
	AutoMin                int64   `json:"auto_min"`
	AutoMax                int64   `json:"auto_max"`
	StartTimeTargetSeconds float64 `json:"start_time_target_seconds"`
}

// Validate checks a complete class policy.
func (w WarmCapacitySettings) Validate() error {
	valid := false
	for _, c := range match.CloudSizes() {
		valid = valid || c.Label == w.Class
	}
	if !valid || (w.Mode != "off" && w.Mode != "manual" && w.Mode != "auto") || w.ManualCount < 0 || w.ManualCount > math.MaxInt32 || w.AutoMin < 0 || w.AutoMax < w.AutoMin || w.AutoMax > math.MaxInt32 || w.StartTimeTargetSeconds <= 0 || math.IsNaN(w.StartTimeTargetSeconds) || math.IsInf(w.StartTimeTargetSeconds, 0) {
		return fmt.Errorf("class, mode, counts in 0..2147483647, ordered auto bounds and a positive target are required")
	}
	return nil
}
