package capacity

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const (
	MinSamples = 3

	DriftFraction = 0.25

	coldStartFraction = 0.5

	measuredCoreFloor = 0.1

	WarmStartMultiple = 1.0

	SafetyMultiple = 2.0

	SustainedPercentile = 0.80
)

type Pin struct {
	Cores       float64
	MemoryBytes int64
}

func (p *Pin) Empty() bool {
	return p == nil || (p.Cores <= 0 && p.MemoryBytes <= 0)
}

type Resolution struct {
	Cores            float64
	MemoryBytes      int64
	Source           store.CostSource
	ExpectedDuration time.Duration
}

// Resolve computes the capacity view's resource estimate for a pipeline.
// A non-empty pin wins verbatim; a measured profile of the run's own version
// (matching plan hash) with at least MinSamples clean samples supplies
// sustained cores and peak memory (the split [store.PipelineProfile]
// explains); a version that changed structurally or has not yet graduated
// takes the larger of a warm start at its predecessor's estimate and a
// safety multiple of its contended-run demand floor; otherwise the
// cold-start default applies. Admission and billing do not read it.
func Resolve(pin *Pin, profile *store.PipelineProfile, numCPU int, planHash string) Resolution {
	var cores, previous float64
	if profile != nil {
		cores, previous = chargedCores(profile), carriedCores(profile)
	}
	return resolve(pin, profile, numCPU, planHash, cores, previous)
}

func resolve(pin *Pin, profile *store.PipelineProfile, numCPU int, planHash string, cores, previous float64) Resolution {
	res := Resolution{}
	if profile != nil {
		res.ExpectedDuration = profile.P50Duration
	}
	if !pin.Empty() {
		res.Cores = pin.Cores
		res.MemoryBytes = pin.MemoryBytes
		res.Source = store.CostSourcePin
		return res
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
	var floorCores float64
	var floorMem int64
	if versionChanged {
		prevCores, prevMem = current, profile.PeakMemoryBytes
		if prevCores == 0 {
			prevCores, prevMem = previous, profile.PrevPeakMemoryBytes
		}
	} else {
		prevCores, prevMem = previous, profile.PrevPeakMemoryBytes
		floorCores, floorMem = profile.FloorCores, profile.FloorMemoryBytes
	}

	cores := coldStartCores(numCPU)
	res.Source = store.CostSourceDefault
	if prevCores > 0 {
		cores = WarmStartMultiple * prevCores
		res.Source = store.CostSourceMeasuring
	}
	if floorCores > 0 {
		if fc := SafetyMultiple * floorCores; res.Source == store.CostSourceDefault || fc > cores {
			cores = fc
			res.Source = store.CostSourceFloor
		}
	}
	res.Cores = math.Max(cores, measuredCoreFloor)

	// safety: saturate unrepresentable demand before configured ceilings are applied.
	floorMemory := int64(math.MaxInt64)
	if floorMem <= math.MaxInt64/2 {
		floorMemory = floorMem * 2
	}
	res.MemoryBytes = max(prevMem, floorMemory)
	return res
}

func coldStartCores(numCPU int) float64 {
	half := math.Ceil(coldStartFraction * float64(numCPU))
	return math.Max(1, half)
}

type DriftClass string

const (
	driftUnderPinned DriftClass = "under_pinned"

	driftOverPinned DriftClass = "over_pinned"
)

type Drift struct {
	Class         DriftClass `json:"class"`
	PinCores      float64    `json:"pin_cores"`
	MeasuredCores float64    `json:"measured_cores"`
	SampleCount   int        `json:"sample_count"`
	Message       string     `json:"message"`
}

func CheckDrift(pin *Pin, profile *store.PipelineProfile) *Drift {
	if pin.Empty() || profile == nil || !profile.CPUMeasured || profile.SampleCount < MinSamples {
		return nil
	}
	if charged := chargedCores(profile); pin.Cores > 0 && charged > 0 {
		return coreDrift(pin.Cores, charged, profile.SampleCount)
	}
	if pin.MemoryBytes > 0 && profile.PeakMemoryBytes > 0 {
		return memoryDrift(pin.MemoryBytes, profile.PeakMemoryBytes, profile.SampleCount)
	}
	return nil
}

func coreDrift(pinCores, measuredCores float64, samples int) *Drift {
	ratio := pinCores / measuredCores
	class, diverged := classify(ratio)
	if !diverged {
		return nil
	}
	return &Drift{
		Class:         class,
		PinCores:      pinCores,
		MeasuredCores: measuredCores,
		SampleCount:   samples,
		Message: fmt.Sprintf(
			"resource pin: %s cores; measured sustained p95 %s cores over %d runs - update or remove the pin",
			trimFloat(pinCores), trimFloat(measuredCores), samples),
	}
}

func memoryDrift(pinBytes, measuredBytes int64, samples int) *Drift {
	ratio := float64(pinBytes) / float64(measuredBytes)
	class, diverged := classify(ratio)
	if !diverged {
		return nil
	}
	return &Drift{
		Class:       class,
		SampleCount: samples,
		Message: fmt.Sprintf(
			"resource pin: %s memory; measured p95 %s over %d runs - update or remove the pin",
			gib(pinBytes), gib(measuredBytes), samples),
	}
}

func classify(ratio float64) (DriftClass, bool) {
	switch {
	case ratio < 1-DriftFraction:
		return driftUnderPinned, true
	case ratio > 1+DriftFraction:
		return driftOverPinned, true
	default:
		return "", false
	}
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func gib(bytes int64) string {
	return trimFloat(math.Round(float64(bytes)/float64(1<<30)*10)/10) + "GB"
}
