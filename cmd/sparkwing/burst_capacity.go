package main

import (
	"context"
	"errors"
	"math"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func burstCapacity(state wingwire.QueueState) (capacity, available match.Resources, err error) {
	for _, r := range state.Resources {
		if math.IsNaN(r.Capacity) || math.IsInf(r.Capacity, 0) || r.Capacity < 0 || math.IsNaN(r.Available) || math.IsInf(r.Available, 0) || r.Available < 0 || r.Available > r.Capacity {
			return capacity, available, errors.New("local admission returned invalid capacity")
		}
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

func observeBurstCapacity(ctx context.Context, home string) (match.Resources, match.Resources, error) {
	state, err := wingdclient.Query(ctx, wingdclient.Options{Home: home, Version: installedVersion()})
	if err != nil {
		return match.Resources{}, match.Resources{}, err
	}
	return burstCapacity(state)
}
