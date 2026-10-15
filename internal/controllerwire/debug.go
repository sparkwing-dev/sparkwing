package controllerwire

import (
	"context"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"k8s.io/apimachinery/pkg/util/validation"
)

// DebugExecution identifies the current executor without exposing claim authority.
type DebugExecution struct {
	Runner    string `json:"runner"`
	Kind      string `json:"kind"`
	Pod       string `json:"pod,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Container string `json:"container,omitempty"`
}

// DebugJob is a public job projection with its current execution location.
type DebugJob struct {
	RunID     string          `json:"run_id"`
	Number    int64           `json:"number"`
	Job       JobDetail       `json:"job"`
	Execution *DebugExecution `json:"execution"`
}

// DebugPause contains masked pause diagnostics for one selected job.
type DebugPause struct {
	Reason    string     `json:"reason"`
	PausedAt  time.Time  `json:"paused_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// DebugPauseResult distinguishes an absent pause from an unsuccessful read.
type DebugPauseResult struct {
	RunID  string      `json:"run_id"`
	Number int64       `json:"number"`
	JobID  string      `json:"job_id"`
	Pause  *DebugPause `json:"pause"`
}

// Validate checks the public job identity and explicit attachment location.
func (v DebugJob) Validate(run, job string) error {
	if !debugRunIdentity(run, v.RunID, v.Number) || v.Job.ID != job || v.Job.Status == "" || v.Job.Generation < 0 || v.Job.Attempt < 0 || v.Job.StateSeq < 0 {
		return invalidDebugProjection()
	}
	if v.Execution != nil {
		return v.Execution.Validate()
	}
	return nil
}

// Validate checks a server-projected executor without interpreting claim-holder strings.
func (v DebugExecution) Validate() error {
	if v.Runner == "" {
		return invalidDebugProjection()
	}
	switch v.Kind {
	case "local", "runner":
		if v.Pod == "" && v.Namespace == "" && v.Container == "" {
			return nil
		}
	case "kubernetes":
		if len(validation.IsDNS1123Subdomain(v.Pod)) == 0 && len(validation.IsDNS1123Label(v.Namespace)) == 0 && len(validation.IsDNS1123Label(v.Container)) == 0 {
			return nil
		}
	}
	return invalidDebugProjection()
}

// Validate checks that an absent or active pause belongs to the selected job.
func (v DebugPauseResult) Validate(run, job string) error {
	if !debugRunIdentity(run, v.RunID, v.Number) || v.JobID != job || v.Pause != nil && (v.Pause.Reason == "" || v.Pause.PausedAt.IsZero() || v.Pause.ExpiresAt != nil && v.Pause.ExpiresAt.Before(v.Pause.PausedAt)) {
		return invalidDebugProjection()
	}
	return nil
}

func debugRunIdentity(selector, id string, number int64) bool {
	return id != "" && number > 0 && (selector == id || selector == strconv.FormatInt(number, 10))
}

func invalidDebugProjection() error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid debug projection"}
}

// DebugReader reads public job and pause projections over HTTP or the local API socket.
type DebugReader interface {
	ReadDebugJob(context.Context, string, string) (DebugJob, error)
	ReadDebugPause(context.Context, string, string) (DebugPauseResult, error)
}
