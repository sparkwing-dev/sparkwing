package controllerwire

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// StepReportRequest names an observed attempt; authorization supplies its actor and current fence.
type StepReportRequest struct {
	StepID        string     `json:"step_id"`
	State         string     `json:"state"`
	SourceAttempt int64      `json:"source_attempt"`
	MaskedError   string     `json:"error,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

// StepRecorded acknowledges persistence before the host emits its step diagnostic.
type StepRecorded struct {
	Recorded bool `json:"recorded"`
}

// Action selects the existing C2 step route without accepting an arbitrary path.
func (r StepReportRequest) Action() (string, error) {
	if r.StepID == "" || r.SourceAttempt <= 0 {
		return "", &apierr.Error{Code: apierr.BadRequest, Message: "step report requires a step and positive source attempt"}
	}
	switch r.State {
	case "running":
		return "start", nil
	case "passed", "failed", "cancelled":
		return "finish", nil
	case "skipped":
		return "skip", nil
	default:
		return "", &apierr.Error{Code: apierr.BadRequest, Message: "invalid step report state"}
	}
}
