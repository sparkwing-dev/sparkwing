package cluster

import (
	"context"
	"errors"
	"math"
	"runtime"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func runnerCapacity(home, version string, config userconfig.RunnerConfig, engines *engine.Resolver) (func(context.Context) (match.Resources, match.Resources, error), error) {
	var contribution, reserve userconfig.Budget
	for _, field := range []struct {
		value  *string
		budget *userconfig.Budget
	}{{config.Contribution, &contribution}, {config.KeepFree, &reserve}} {
		if field.value != nil {
			value, err := userconfig.ParseBudget(*field.value)
			if err != nil {
				return nil, err
			}
			*field.budget = value
		}
	}
	local := config.LocalAdmission != nil && *config.LocalAdmission
	sampler := wingd.NewHostSampler()
	return func(ctx context.Context) (match.Resources, match.Resources, error) {
		if err := context.Cause(ctx); err != nil {
			return match.Resources{}, match.Resources{}, err
		}
		var capacity, available match.Resources
		var err error
		if local {
			capacity, available, err = runnerLocalCapacity(ctx, home, version, engines.Resolve, wingdclient.Query, wingdclient.EnsureLocalProcess)
		} else {
			var stat wingd.HostStat
			stat, err = sampler.Sample()
			if err == nil {
				capacity, available, err = sampledCapacity(stat)
			}
		}
		if err != nil {
			return capacity, available, err
		}
		capacity, available = runnerShare(capacity, available, contribution, reserve)
		return capacity, available, nil
	}, nil
}

func runnerLocalCapacity(ctx context.Context, home, version string,
	resolve func(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error),
	query func(context.Context, wingdclient.Options) (wingwire.QueueState, error),
	start func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error),
) (match.Resources, match.Resources, error) {
	options := wingdclient.Options{Home: home, Version: version}
	state, err := query(ctx, options)
	if errors.Is(err, wingdclient.ErrNoDaemon) {
		selected, resolveErr := resolve(ctx, engine.Pin{SDK: engine.GoSDK, Version: version}, releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
		if resolveErr != nil {
			return match.Resources{}, match.Resources{}, resolveErr
		}
		if _, err = start(ctx, wingd.LocalStartOptions{Home: home, Version: version, Executable: selected.Executable}); err == nil {
			state, err = query(ctx, options)
		}
	}
	if err != nil {
		return match.Resources{}, match.Resources{}, err
	}
	return wingdclient.Capacity(state)
}

func sampledCapacity(stat wingd.HostStat) (match.Resources, match.Resources, error) {
	if stat.TotalCores <= 0 || math.IsNaN(stat.TotalCores) || math.IsInf(stat.TotalCores, 0) || stat.TotalMemoryBytes >= math.MaxInt64 {
		return match.Resources{}, match.Resources{}, errors.New("runner observed invalid machine capacity")
	}
	capacity := match.Resources{Cores: stat.TotalCores, MemoryBytes: int64(stat.TotalMemoryBytes)}
	available := capacity
	if stat.CPUMeasured {
		available.Cores = max(0, capacity.Cores-stat.BusyCores)
	} else if stat.LoadMeasured {
		available.Cores = max(0, capacity.Cores-stat.LoadAverage)
	}
	if stat.MemoryMeasured {
		available.MemoryBytes = int64(min(stat.FreeMemoryBytes, stat.TotalMemoryBytes))
	}
	if math.IsNaN(available.Cores) || math.IsInf(available.Cores, 0) || available.Cores > capacity.Cores {
		return match.Resources{}, match.Resources{}, errors.New("runner observed invalid machine headroom")
	}
	return capacity, available, nil
}

func runnerShare(capacity, available match.Resources, contribution, reserve userconfig.Budget) (match.Resources, match.Resources) {
	reservedCores, reservedMemory := float64(0), uint64(0)
	if reserve.Cores > 0 || reserve.CoresFraction > 0 {
		reservedCores = reserve.CapCores(capacity.Cores)
	}
	if reserve.MemoryBytes > 0 || reserve.MemoryFraction > 0 {
		reservedMemory = reserve.CapMemory(uint64(capacity.MemoryBytes))
	}
	limit := match.Resources{
		Cores:       min(contribution.CapCores(capacity.Cores), max(0, capacity.Cores-reservedCores)),
		MemoryBytes: min(int64(contribution.CapMemory(uint64(capacity.MemoryBytes))), capacity.MemoryBytes-int64(reservedMemory)),
	}
	available.Cores = min(limit.Cores, max(0, available.Cores-reservedCores))
	available.MemoryBytes = min(limit.MemoryBytes, max(0, available.MemoryBytes-int64(reservedMemory)))
	return capacity, available
}
