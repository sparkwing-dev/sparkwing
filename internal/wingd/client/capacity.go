package client

import (
	"errors"
	"math"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// Capacity reads total and free resources from an admission observation.
func Capacity(state wingwire.QueueState) (capacity, available match.Resources, err error) {
	seen := map[string]bool{}
	for _, r := range state.Resources {
		if seen[r.Key] || math.IsNaN(r.Capacity) || math.IsInf(r.Capacity, 0) || r.Capacity < 0 || math.IsNaN(r.Available) || math.IsInf(r.Available, 0) || r.Available < 0 || r.Available > r.Capacity {
			return capacity, available, errors.New("local admission returned invalid capacity")
		}
		seen[r.Key] = true
		switch r.Key {
		case "cores":
			capacity.Cores, available.Cores = r.Capacity, r.Available
		case "memory":
			if r.Capacity >= math.MaxInt64 {
				return capacity, available, errors.New("local admission returned invalid memory capacity")
			}
			capacity.MemoryBytes, available.MemoryBytes = int64(r.Capacity), int64(r.Available)
		}
	}
	if capacity.Cores <= 0 {
		return capacity, available, errors.New("local admission did not report CPU capacity")
	}
	return capacity, available, nil
}
