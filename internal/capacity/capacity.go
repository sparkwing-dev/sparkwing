// Package capacity resolves a run's admission cost from measurement. The
// authoritative order is: an explicit .Resources() pin wins; else a
// measured profile once it has enough samples; else a conservative
// cold-start default that biases toward serializing an unknown pipeline's
// first runs. It also polices pins, warning when one has drifted far from
// what the pipeline actually costs.
//
// The functions here are pure so the resolution table and the
// drift-warning gating can be tested without a store or a daemon; the
// orchestrator supplies the pin, the measured profile, and the machine
// size.
package capacity

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	// MinSamples is how many measured runs a profile needs before
	// admission trusts it over the cold-start default, and before a pin is
	// judged against it. Small enough to learn fast, large enough that one
	// odd run cannot flip a decision.
	MinSamples = 5
	// DriftFraction is the relative gap between a pin and the measured p99
	// peak that trips a drift warning. Below it, the pin and reality agree
	// closely enough to stay quiet.
	DriftFraction = 0.25
	// coldStartFraction is the share of the machine an unknown execution
	// shape receives until the sampler has enough clean observations.
	coldStartFraction = 1.0
	// measuredCoreFloor is the minimum core charge for a measured profile,
	// so a pipeline the sampler observed drawing near-zero CPU (a poller,
	// approval waiter, or lock holder) is still accounted for rather than
	// admitted for free, while costing far less than the cold-start default.
	measuredCoreFloor = 0.1
	// SafetyMultiple prices a still-measuring version: its charge is this
	// multiple of the best evidence available -- the predecessor peak across a
	// structural change, or the demand floor learned from contended runs. It
	// sets ramp speed, not safety: ceiling-hit escalation makes each contended
	// run that consumes its whole charge double the next, a log2 search that
	// converges on true demand from below, the same doubling reasoning as TCP
	// slow-start. One named constant so it stays tunable.
	SafetyMultiple = 2.0
	// CeilingHitFraction is how much of its admitted charge a contended run
	// must consume for the charge to count as a proven demand minimum: at or
	// above this fraction the run wanted at least its whole charge, so the
	// floor rises to the charge and the next run doubles. Below it, the run's
	// measured peak alone raises the floor and the charge does not escalate.
	CeilingHitFraction = 0.9
	// CacheDominantFraction is the share of a run's completed nodes that must
	// be cache hits for the run to count as cache-dominant and be excluded from
	// profile learning. A run at or above this threshold measured the cache,
	// not the work -- its wall time collapses and its CPU is near zero -- so
	// folding it would poison durations and age real peaks out of the window,
	// the same measurement contamination contention already guards against.
	CacheDominantFraction = 0.9
)

// Pin is an explicit .Resources() declaration flattened to host figures.
// A nil Pin means the pipeline declared nothing.
type Pin struct {
	Cores       float64
	MemoryBytes int64
}

// Empty reports whether the pin declares neither cores nor memory.
func (p *Pin) Empty() bool {
	return p == nil || (p.Cores <= 0 && p.MemoryBytes <= 0)
}

// Resolution is the resolved admission cost plus its provenance and the
// expected duration ETA uses. ExpectedDuration is zero when no measured
// profile backs it.
type Resolution struct {
	Cores            float64
	MemoryBytes      int64
	Source           store.CostSource
	ExpectedDuration time.Duration
}

func ApplyUnknownHostEnvelope(res Resolution, grantableCores float64, grantableMemoryBytes int64) Resolution {
	if res.Source != store.CostSourceDefault {
		return res
	}
	if grantableCores > 0 {
		res.Cores = grantableCores
	}
	if grantableMemoryBytes > 0 {
		res.MemoryBytes = grantableMemoryBytes
	}
	return res
}

// Resolve applies the resolution order. A non-empty pin wins verbatim; a
// measured profile of the run's own version (matching plan hash) with at
// least MinSamples clean samples supplies the measured peaks; a version that
// changed structurally or has not yet graduated is priced by measurement --
// a safety multiple of its predecessor peak or its contended-run demand
// floor, whichever is larger; otherwise the cold-start default is charged.
// ExpectedDuration is filled from the profile whenever one exists, even when
// a pin sets the cost, so ETA still has a duration to simulate with.
//
// planHash is the DAG-topology fingerprint of the version being admitted.
// When it differs from the stored profile's hash the pipeline changed
// structurally and its measured peaks no longer describe it, so admission
// re-measures from the predecessor rather than pricing on stale samples. An
// empty planHash disables version tracking (per-node and cluster paths that
// do not carry one), keeping the pin-measured-default order.
//
// A profile qualifies as measured on sample count plus evidence the
// sampler was not blind: either a positive peak, or a healthy sampler
// (CPUMeasured) that observed a genuine near-zero peak. A near-zero
// measured pipeline is charged its measured memory plus a small core
// floor, so quiet pollers and lock holders admit at their true tiny cost
// instead of queueing behind the conservative default forever. A blind
// sampler's zero never qualifies, keeping the conservative default.
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
		res.Cores = math.Max(profile.PeakCores, measuredCoreFloor)
		res.MemoryBytes = profile.PeakMemoryBytes
		res.Source = store.CostSourceMeasured
		return res
	}
	return measuringResolution(res, profile, numCPU, versionChanged)
}

// measuringResolution prices a version that has not finalized a measured
// price for the run's structure: one that changed shape, or one still short
// of MinSamples clean runs. The charge is the largest of a warm start (a
// safety multiple of the predecessor peak, else the half-machine default for
// a pipeline with no prior measurement), the safety multiple of the demand
// floor its own contended runs proved, and the small absolute core floor.
// The floor's evidence belongs to the current version only, so a structural
// change ignores it and re-measures from the predecessor.
func measuringResolution(res Resolution, profile *store.PipelineProfile, numCPU int, versionChanged bool) Resolution {
	var prevCores float64
	var prevMem int64
	var floorCores float64
	var floorMem int64
	if versionChanged {
		prevCores, prevMem = profile.PeakCores, profile.PeakMemoryBytes
		if prevCores == 0 {
			prevCores, prevMem = profile.PrevPeakCores, profile.PrevPeakMemoryBytes
		}
	} else {
		prevCores, prevMem = profile.PrevPeakCores, profile.PrevPeakMemoryBytes
		floorCores, floorMem = profile.FloorCores, profile.FloorMemoryBytes
	}

	cores := coldStartCores(numCPU)
	res.Source = store.CostSourceDefault
	if prevCores > 0 {
		cores = SafetyMultiple * prevCores
		res.Source = store.CostSourceMeasuring
	}
	if floorCores > 0 {
		if fc := SafetyMultiple * floorCores; res.Source == store.CostSourceDefault || fc > cores {
			cores = fc
			res.Source = store.CostSourceFloor
		}
	}
	res.Cores = math.Max(cores, measuredCoreFloor)

	mem := int64(SafetyMultiple * float64(prevMem))
	if fm := int64(SafetyMultiple * float64(floorMem)); fm > mem {
		mem = fm
	}
	res.MemoryBytes = mem
	return res
}

// ApplyHostCeiling caps measured/default CPU charge at the machine's idle
// grantable ceiling so oversized measured CPU serializes alone. Explicit CPU
// pins and all memory demand are left intact; admission enforces both as hard
// budgets. A non-positive CPU ceiling or a charge already within it leaves the
// resolution unchanged and returns no warning.
func ApplyHostCeiling(res Resolution, machineCores, grantableCores float64, grantableMemoryBytes int64) (Resolution, string) {
	warning := ""
	if res.Source == store.CostSourcePin {
		if machineCores > 0 && res.Cores > machineCores {
			warning = fmt.Sprintf("pin %.1f cores exceeds this machine (%.1f); use a smaller pin or a larger machine",
				res.Cores, machineCores)
		}
		return res, warning
	}
	if grantableCores > 0 && res.Cores > grantableCores {
		res.Cores = grantableCores
	}
	return res, warning
}

// measurementQualifies reports whether a profile has enough evidence to
// cost a run by measurement rather than the cold-start default: at least
// MinSamples observations, and either a positive measured peak or a
// healthy sampler that recorded a real near-zero peak. A blind sampler's
// zero peak (CPUMeasured false, PeakCores zero) never qualifies.
func measurementQualifies(profile *store.PipelineProfile) bool {
	return profile != nil && profile.SampleCount >= MinSamples &&
		(profile.PeakCores > 0 || profile.CPUMeasured)
}

// coldStartCores is the conservative charge for an unknown pipeline: half
// the machine, never below one core.
func coldStartCores(numCPU int) float64 {
	cores := math.Ceil(coldStartFraction * float64(numCPU))
	return math.Max(1, cores)
}

// DriftClass names how a pin has diverged from measurement.
type DriftClass string

const (
	// DriftUnderPinned marks a pin set well below the measured peak: the
	// run is charged less than it uses, so the machine oversubscribes.
	DriftUnderPinned DriftClass = "under_pinned"
	// DriftOverPinned marks a pin set far above the measured peak: capacity
	// is reserved and never used, needlessly queueing other work.
	DriftOverPinned DriftClass = "over_pinned"
)

// Drift describes a pin that has drifted from measured reality, with a
// one-line message carrying the exact fix.
type Drift struct {
	Class         DriftClass `json:"class"`
	PinCores      float64    `json:"pin_cores"`
	MeasuredCores float64    `json:"measured_cores"`
	SampleCount   int        `json:"sample_count"`
	Message       string     `json:"message"`
}

// CheckDrift compares an explicit pin against the measured profile and
// returns a warning when they diverge past DriftFraction. It returns nil
// -- never warns -- for an unpinned pipeline, a profile with fewer than
// MinSamples, or a pin that agrees with measurement. Cores drive the
// comparison; a memory-only pin falls back to the memory dimension.
func CheckDrift(pin *Pin, profile *store.PipelineProfile) *Drift {
	if pin.Empty() || profile == nil || profile.SampleCount < MinSamples {
		return nil
	}
	if pin.Cores > 0 && profile.PeakCores > 0 {
		return coreDrift(pin.Cores, profile.PeakCores, profile.SampleCount)
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
			"resource pin: %s cores; measured p99 %s cores over %d runs - update or remove the pin",
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
			"resource pin: %s memory; measured p99 %s over %d runs - update or remove the pin",
			gib(pinBytes), gib(measuredBytes), samples),
	}
}

// classify maps a pin/measured ratio to a drift class, reporting whether
// it is past the threshold in either direction.
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
