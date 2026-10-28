package controllerwire

import (
	"encoding/json"
	"time"
)

// PauseHistory contains the fields exposed by the controller.
type PauseHistory struct {
	RunID       string     `json:"run_id"`
	NodeID      string     `json:"node_id"`
	Reason      string     `json:"reason"`
	PausedAt    time.Time  `json:"paused_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ReleasedAt  *time.Time `json:"released_at,omitempty"`
	ReleasedBy  string     `json:"released_by,omitempty"`
	ReleaseKind string     `json:"release_kind,omitempty"`
}

// PipelineSummary contains the fields exposed by the controller.
type PipelineSummary struct {
	Name           string     `json:"name"`
	LastActivityAt time.Time  `json:"last_activity_at,omitzero"`
	LastRunID      string     `json:"last_run_id,omitempty"`
	LastStatus     string     `json:"last_status,omitempty"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastFinishedAt *time.Time `json:"last_finished_at,omitempty"`
}

// PipelineProfile contains the fields exposed by the controller.
type PipelineProfile struct {
	Pipeline            string        `json:"pipeline"`
	NodeID              string        `json:"node_id"`
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

// CredentialRotation contains the fields exposed by the controller.
type CredentialRotation struct {
	Rotated        int                  `json:"rotated"`
	Skipped        []SecretRotationSkip `json:"skipped"`
	GitCredentials int                  `json:"git_credentials"`
}

// SecretRotationSkip contains the fields exposed by the controller.
type SecretRotationSkip struct {
	Name     string `json:"name"`
	Repo     string `json:"repo,omitempty"`
	Pipeline string `json:"pipeline,omitempty"`
}

// GitHubAppAutomation contains the fields exposed by the controller.
type GitHubAppAutomation struct {
	RepositoryID   int64  `json:"repository_id"`
	Repository     string `json:"repository"`
	InstallationID int64  `json:"installation_id"`
	Enabled        bool   `json:"enabled"`
	EnabledBy      string `json:"enabled_by"`
	Runner         string `json:"runner"`
}

// RunEvent contains the fields exposed by the controller.
type RunEvent struct {
	RunID   string          `json:"run_id"`
	Seq     int64           `json:"seq"`
	NodeID  string          `json:"node_id,omitempty"`
	Kind    string          `json:"kind"`
	TS      time.Time       `json:"ts"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// StepObservation contains the fields exposed by the controller.
type StepObservation struct {
	RunID       string       `json:"run_id,omitempty"`
	NodeID      string       `json:"node_id"`
	StepID      string       `json:"step_id"`
	Attempt     int64        `json:"attempt"`
	Status      string       `json:"status"`
	Error       string       `json:"error,omitempty"`
	StartedAt   *time.Time   `json:"started_at,omitempty"`
	FinishedAt  *time.Time   `json:"finished_at,omitempty"`
	Annotations []Annotation `json:"annotations,omitempty"`
	Summary     string       `json:"summary,omitempty"`
}
