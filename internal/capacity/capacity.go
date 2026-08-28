package capacity

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	MinSamples = 3

	DriftFraction = 0.25

	coldStartFraction = 0.5

	measuredCoreFloor = 0.1

	WarmStartMultiple = 1.0

	SafetyMultiple = 2.0

	SustainedPercentile = 0.80

	CeilingHitFraction = 0.9

	CacheDominantFraction = 0.9
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

// Resolve applies the resolution order. A non-empty pin wins verbatim; a
// measured profile of the run's own version (matching plan hash) with at
// least MinSamples clean samples supplies the measured charge, sustained
// cores and peak memory (the split [store.PipelineProfile] explains); a
// version that changed structurally or has not yet graduated is priced by
// measurement -- a warm start at what its predecessor was charged or a
// safety multiple of its contended-run demand floor, whichever is larger;
// otherwise the cold-start default is charged.
// ExpectedDuration is filled from the profile whenever one exists, even when
// a pin sets the cost, so ETA still has a duration to simulate with.
func Resolve(pin *Pin, profile *store.PipelineProfile, numCPU int, planHash string) Resolution {
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
	if profile == nil {
		res.Cores = coldStartCores(numCPU)
		res.Source = store.CostSourceDefault
		return res
	}
	versionChanged := planHash != "" && profile.PlanHash != "" && profile.PlanHash != planHash
	if !versionChanged && measurementQualifies(profile) {
		res.Cores = math.Max(chargedCores(profile), measuredCoreFloor)
		res.MemoryBytes = profile.PeakMemoryBytes
		res.Source = store.CostSourceMeasured
		return res
	}
	return measuringResolution(res, profile, numCPU, versionChanged)
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

func measuringResolution(res Resolution, profile *store.PipelineProfile, numCPU int, versionChanged bool) Resolution {
	var prevCores float64
	var prevMem int64
	var floorCores float64
	var floorMem int64
	if versionChanged {
		prevCores, prevMem = chargedCores(profile), profile.PeakMemoryBytes
		if prevCores == 0 {
			prevCores, prevMem = carriedCores(profile), profile.PrevPeakMemoryBytes
		}
	} else {
		prevCores, prevMem = carriedCores(profile), profile.PrevPeakMemoryBytes
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

	mem := int64(WarmStartMultiple * float64(prevMem))
	if fm := int64(SafetyMultiple * float64(floorMem)); fm > mem {
		mem = fm
	}
	res.MemoryBytes = mem
	return res
}

func ApplyHostCeiling(res Resolution, pipeline string, machineCores, grantableCores float64, grantableMemoryBytes int64) (Resolution, string) {
	warning := ""
	if res.Source == store.CostSourcePin {
		if machineCores > 0 && res.Cores > machineCores {
			warning = fmt.Sprintf("pin %.1f cores exceeds this machine (%.1f); use a smaller pin or a larger machine",
				res.Cores, machineCores)
		}
		return res, warning
	}
	measuring := res.Source == store.CostSourceFloor || res.Source == store.CostSourceMeasuring
	if grantableCores > 0 && res.Cores > grantableCores {
		if measuring {
			warning = fmt.Sprintf("measuring charge %.1f cores exceeds this machine's grantable %.1f, so runs are admitted alone; if contention poisoned the profile, reset it: sparkwing runs stats --reset --pipeline %s",
				res.Cores, grantableCores, pipeline)
		}
		res.Cores = grantableCores
	}
	if grantableMemoryBytes > 0 && res.MemoryBytes > grantableMemoryBytes {
		if measuring && warning == "" {
			warning = fmt.Sprintf("measuring charge %s exceeds this machine's grantable %s, so runs are admitted alone; if contention poisoned the profile, reset it: sparkwing runs stats --reset --pipeline %s",
				gib(res.MemoryBytes), gib(grantableMemoryBytes), pipeline)
		}
		res.MemoryBytes = grantableMemoryBytes
	}
	return res, warning
}

func measurementQualifies(profile *store.PipelineProfile) bool {
	return profile != nil && profile.SampleCount >= MinSamples &&
		(profile.PeakCores > 0 || profile.CPUMeasured)
}

func FloorPoisoned(profile *store.PipelineProfile, grantableCores float64) bool {
	if profile == nil || grantableCores <= 0 {
		return false
	}
	if profile.PinnedCores > 0 || profile.PinnedMemoryBytes > 0 {
		return false
	}
	if measurementQualifies(profile) {
		return false
	}
	return profile.FloorCores > 0 && SafetyMultiple*profile.FloorCores >= grantableCores
}

func coldStartCores(numCPU int) float64 {
	half := math.Ceil(coldStartFraction * float64(numCPU))
	return math.Max(1, half)
}

type DriftClass string

const (
	DriftUnderPinned DriftClass = "under_pinned"

	DriftOverPinned DriftClass = "over_pinned"
)

type Drift struct {
	Class         DriftClass `json:"class"`
	PinCores      float64    `json:"pin_cores"`
	MeasuredCores float64    `json:"measured_cores"`
	SampleCount   int        `json:"sample_count"`
	Message       string     `json:"message"`
}

func CheckDrift(pin *Pin, profile *store.PipelineProfile) *Drift {
	if pin.Empty() || profile == nil || profile.SampleCount < MinSamples {
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
		return DriftUnderPinned, true
	case ratio > 1+DriftFraction:
		return DriftOverPinned, true
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
