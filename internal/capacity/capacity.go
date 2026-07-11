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
	MinSamples = 3
	// DriftFraction is the relative gap between a pin and the measured p99
	// peak that trips a drift warning. Below it, the pin and reality agree
	// closely enough to stay quiet.
	DriftFraction = 0.25
	// coldStartFraction is the share of the machine an unknown pipeline's
	// first run is charged. Half the machine means two unknown runs cannot
	// both hold capacity at once, so unknown heavy work serializes until
	// the sampler has profiled it.
	coldStartFraction = 0.5
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

// Resolve applies the resolution order. A non-empty pin wins verbatim; a
// measured profile with at least MinSamples supplies the measured peaks;
// otherwise the cold-start default is charged. ExpectedDuration is filled
// from the profile whenever one exists, even when a pin sets the cost, so
// ETA still has a duration to simulate with.
func Resolve(pin *Pin, profile *store.PipelineProfile, numCPU int) Resolution {
	res := Resolution{}
	if profile != nil {
		res.ExpectedDuration = profile.P50Duration
	}
	switch {
	case !pin.Empty():
		res.Cores = pin.Cores
		res.MemoryBytes = pin.MemoryBytes
		res.Source = store.CostSourcePin
	case profile != nil && profile.SampleCount >= MinSamples && profile.PeakCores > 0:
		res.Cores = profile.PeakCores
		res.MemoryBytes = profile.PeakMemoryBytes
		res.Source = store.CostSourceMeasured
	default:
		res.Cores = coldStartCores(numCPU)
		res.Source = store.CostSourceDefault
	}
	return res
}

// coldStartCores is the conservative charge for an unknown pipeline: half
// the machine, never below one core.
func coldStartCores(numCPU int) float64 {
	half := math.Ceil(coldStartFraction * float64(numCPU))
	return math.Max(1, half)
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
			"resource pin: %s cores; measured p99 %s cores over %d runs — update or remove the pin",
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
			"resource pin: %s memory; measured p99 %s over %d runs — update or remove the pin",
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
