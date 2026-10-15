package capability

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// RunnerHeadroom carries accepted capacity and its store-owned observation time.
type RunnerHeadroom struct {
	Cores       float64
	MemoryBytes int64
	QueueDepth  int
	ObservedAt  time.Time
}

// RunnerObservation is one tenant's granted runner identity and committed activity.
type RunnerObservation struct {
	Team          string
	Runner        nodeproto.RunnerIdentity
	Location      string
	LastSeen      time.Time
	ActiveJobs    []string
	MaxConcurrent int
	Headroom      *RunnerHeadroom
}

// RunnerSnapshot reads live enrollment, activity and time in one store snapshot.
type RunnerSnapshot struct {
	Now     time.Time
	Runners []RunnerObservation
}
