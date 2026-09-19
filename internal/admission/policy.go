package admission

import "time"

// Mode selects how host-capacity admission decisions are made. Semaphore
// claims remain deterministic in every mode.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeAuto   Mode = "auto"
	ModeJev    Mode = "jev"
	ModeCustom Mode = "custom"
)

// WorkloadClass describes how quickly a waiting request should regain its
// reserved capacity. It is separate from Priority, which is an explicit
// operator ordering override.
type WorkloadClass string

const (
	ClassCritical    WorkloadClass = "critical"
	ClassInteractive WorkloadClass = "interactive"
	ClassNormal      WorkloadClass = "normal"
	ClassBatch       WorkloadClass = "batch"
)

// Policy controls duration-aware backfill. A zero-value policy preserves the
// original single-backfill behavior.
type SchedulingPolicy struct {
	BackfillDelay map[WorkloadClass]time.Duration
	ClassWeight   map[WorkloadClass]int
	// AgingEvery raises a waiter's class score by one after this many newer
	// arrivals. Zero disables class-weighted ordering.
	AgingEvery uint64
	Burst      BurstPolicy
}

// BurstPolicy bounds the single CPU-only lane for well-profiled interactive
// work. Memory and semaphores never burst.
type BurstPolicy struct {
	MaxCores   float64
	MaxP99     time.Duration
	MinSamples int
}

// AutoPolicy favors short interactive work while placing a finite bound on
// how much measured work may pass an older waiter.
func AutoPolicy() SchedulingPolicy {
	return SchedulingPolicy{BackfillDelay: map[WorkloadClass]time.Duration{
		ClassCritical:    0,
		ClassInteractive: 2 * time.Second,
		ClassNormal:      10 * time.Second,
		ClassBatch:       30 * time.Second,
	}, ClassWeight: map[WorkloadClass]int{
		ClassCritical:    12,
		ClassInteractive: 8,
		ClassNormal:      4,
		ClassBatch:       0,
	}, AgingEvery: 4, Burst: BurstPolicy{MaxCores: 1, MaxP99: 2 * time.Second, MinSamples: 3}}
}

func normalizeClass(class WorkloadClass) WorkloadClass {
	switch class {
	case ClassCritical, ClassInteractive, ClassNormal, ClassBatch:
		return class
	default:
		return ClassNormal
	}
}

// NormalizeWorkloadClass maps an empty or unknown wire value to normal work.
func NormalizeWorkloadClass(class WorkloadClass) WorkloadClass {
	return normalizeClass(class)
}

func (p SchedulingPolicy) backfillDelayMS(class WorkloadClass) int64 {
	if p.BackfillDelay == nil {
		return 0
	}
	d := p.BackfillDelay[normalizeClass(class)]
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// BackfillDelayFor reports the configured cumulative measured delay for a class.
func (p SchedulingPolicy) BackfillDelayFor(class WorkloadClass) time.Duration {
	return time.Duration(p.backfillDelayMS(class)) * time.Millisecond
}
