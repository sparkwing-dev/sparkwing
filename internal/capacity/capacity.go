package capacity

import (
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const (
	MinSamples = 3

	coldStartFraction = 0.5

	measuredCoreFloor = 0.1

	WarmStartMultiple = 1.0

	SustainedPercentile = 0.80
)

type Resolution struct {
	Cores            float64
	MemoryBytes      int64
	Source           store.CostSource
	ExpectedDuration time.Duration
}

// Resolve computes the capacity view's resource estimate for a pipeline.
// A measured profile of the run's own version with at least MinSamples
// complete samples supplies sustained cores and peak memory. A changed
// version uses its predecessor's estimate while collecting new samples;
// otherwise the cold-start default applies. Admission and billing do not read it.
func Resolve(profile *store.PipelineProfile, numCPU int, planHash string) Resolution {
	var cores, previous float64
	if profile != nil {
		cores, previous = chargedCores(profile), carriedCores(profile)
	}
	res := Resolution{}
	if profile != nil {
		res.ExpectedDuration = profile.P50Duration
	}
	if profile == nil || !profile.CPUMeasured {
		res.Cores = coldStartCores(numCPU)
		res.Source = store.CostSourceDefault
		return res
	}
	versionChanged := planHash != "" && profile.PlanHash != "" && profile.PlanHash != planHash
	if !versionChanged && profile.SampleCount >= MinSamples {
		res.Cores = math.Max(cores, measuredCoreFloor)
		res.MemoryBytes = profile.PeakMemoryBytes
		res.Source = store.CostSourceMeasured
		return res
	}
	return measuringResolution(res, profile, numCPU, versionChanged, cores, previous)
}

func chargedCores(profile *store.PipelineProfile) float64 {
	if profile.SustainedCores > 0 {
		return profile.SustainedCores
	}
	return profile.PeakCores
}

func carriedCores(profile *store.PipelineProfile) float64 {
	if profile.PrevSustainedCores > 0 {
		return profile.PrevSustainedCores
	}
	return profile.PrevPeakCores
}

func measuringResolution(res Resolution, profile *store.PipelineProfile, numCPU int, versionChanged bool, current, previous float64) Resolution {
	var prevCores float64
	var prevMem int64
	if versionChanged {
		prevCores, prevMem = current, profile.PeakMemoryBytes
		if prevCores == 0 {
			prevCores, prevMem = previous, profile.PrevPeakMemoryBytes
		}
	} else {
		prevCores, prevMem = previous, profile.PrevPeakMemoryBytes
	}

	cores := coldStartCores(numCPU)
	res.Source = store.CostSourceDefault
	if prevCores > 0 {
		cores = WarmStartMultiple * prevCores
		res.Source = store.CostSourceMeasuring
	}
	res.Cores = math.Max(cores, measuredCoreFloor)

	res.MemoryBytes = prevMem
	return res
}

func coldStartCores(numCPU int) float64 {
	half := math.Ceil(coldStartFraction * float64(numCPU))
	return math.Max(1, half)
}
