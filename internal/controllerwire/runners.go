package controllerwire

import "context"

type RunnerResources struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
}

// RunnerHeadroom is an observation accepted by the controller, not a grant of
// capacity or authority. Absence means no current measurement is available.
type RunnerHeadroom struct {
	RunnerResources
	QueueDepth int    `json:"queue_depth"`
	ObservedAt string `json:"observed_at"`
}

// RunnerJob retains both identities rather than encoding a job into a run ID.
// JobID is absent while the runner hosts the run itself.
type RunnerJob struct {
	RunID string `json:"run_id"`
	JobID string `json:"job_id,omitempty"`
}

// RunnerDetail separates granted identity/reservations and billing from free
// labels and observed capacity. Displaying a label never makes it a grant.
type RunnerDetail struct {
	Name          string          `json:"name"`
	Class         string          `json:"class"`
	Location      string          `json:"location"`
	Labels        []string        `json:"labels"`
	ReservedPools []string        `json:"reserved_pools"`
	Repos         []string        `json:"repos"`
	LastSeen      string          `json:"last_seen"`
	Status        string          `json:"status"`
	ActiveJobs    []RunnerJob     `json:"active_jobs"`
	MaxConcurrent int             `json:"max_concurrent"`
	Metered       bool            `json:"metered"`
	Capacity      RunnerResources `json:"capacity"`
	Headroom      *RunnerHeadroom `json:"headroom,omitempty"`
}

type Runners struct {
	Runners []RunnerDetail `json:"runners"`
}

// RunnerReader shares public runner observations between local and remote
// controllers. Registration, grants and claim matching remain server decisions.
type RunnerReader interface {
	ListRunners(context.Context) (Runners, error)
}
