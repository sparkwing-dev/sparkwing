package controllerwire

import (
	"context"
	"time"
)

// SizingMeasurements contains display measurements, not admission authority.
// The controller chooses Source and Drift on SizingDetail using its policy.
type SizingMeasurements struct {
	P50Duration         time.Duration `json:"p50_duration_ns"`
	P99Duration         time.Duration `json:"p99_duration_ns"`
	PeakCores           float64       `json:"peak_cores"`
	PeakMemoryBytes     int64         `json:"peak_memory_bytes"`
	SustainedCores      float64       `json:"sustained_cores"`
	SampleCount         int           `json:"sample_count"`
	CPUP50              float64       `json:"cpu_p50,omitempty"`
	CPUP95              float64       `json:"cpu_p95,omitempty"`
	MemoryP50Bytes      int64         `json:"memory_p50_bytes,omitempty"`
	MemoryP95Bytes      int64         `json:"memory_p95_bytes,omitempty"`
	WaitP50             time.Duration `json:"wait_p50_ns,omitempty"`
	WaitP99             time.Duration `json:"wait_p99_ns,omitempty"`
	WaitSampleCount     int           `json:"wait_sample_count,omitempty"`
	CPUMeasured         bool          `json:"cpu_measured"`
	UpdatedAt           time.Time     `json:"updated_at"`
	PinnedCores         float64       `json:"pinned_cores,omitempty"`
	PinnedMemoryBytes   int64         `json:"pinned_memory_bytes,omitempty"`
	ContendedCount      int           `json:"contended_count,omitempty"`
	PlanHash            string        `json:"plan_hash,omitempty"`
	FloorCores          float64       `json:"floor_cores,omitempty"`
	FloorMemoryBytes    int64         `json:"floor_memory_bytes,omitempty"`
	PrevPeakCores       float64       `json:"prev_peak_cores,omitempty"`
	PrevPeakMemoryBytes int64         `json:"prev_peak_memory_bytes,omitempty"`
	PrevSustainedCores  float64       `json:"prev_sustained_cores,omitempty"`
}

type JobSizing struct {
	JobID string `json:"job_id"`
	SizingMeasurements
}

type SizingDetail struct {
	Pipeline     string             `json:"pipeline"`
	Repo         string             `json:"repo"`
	Source       string             `json:"source"`
	Drift        string             `json:"drift,omitempty"`
	MemoExcluded int                `json:"memo_excluded"`
	Rollup       SizingMeasurements `json:"rollup"`
	Jobs         []JobSizing        `json:"jobs"`
}

type SizingQuery struct {
	Pipeline string
	Limit    int
	Cursor   string
}

type SizingPage struct {
	Sizing     []SizingDetail `json:"sizing"`
	NextCursor string         `json:"next_cursor"`
	Truncated  bool           `json:"truncated"`
}

// SizingReset selects learned samples and floors only. Explicit resource pins
// survive reset; repository authorization and team come from the controller.
type SizingReset struct {
	Pipeline     string `json:"pipeline,omitempty"`
	AllPipelines bool   `json:"all_pipelines"`
}

type SizingResetSummary struct {
	Pipelines      []string `json:"pipelines"`
	RowsDeleted    int64    `json:"rows_deleted"`
	RowsCleared    int64    `json:"rows_cleared"`
	SamplesDropped int64    `json:"samples_dropped"`
	FloorsDropped  int64    `json:"floors_dropped"`
}

// SizingManager reads controller-owned measurements. Reset needs a C2 route;
// until one exists the HTTP adapter returns policy_missing without a request.
type SizingManager interface {
	ListSizing(context.Context, SizingQuery) (SizingPage, error)
	ResetSizing(context.Context, SizingReset) (SizingResetSummary, error)
}
