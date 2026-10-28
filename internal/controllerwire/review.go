package controllerwire

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// AnnotationWrite addresses a job or one of its steps; authority comes from C2.
type AnnotationWrite struct {
	nodeproto.Annotation
	StepID string `json:"step_id,omitempty"`
}

// Approval exposes a gate's public history without store or credential fields.
type Approval struct {
	RunID       string     `json:"run_id"`
	JobID       string     `json:"job_id"`
	RequestedAt time.Time  `json:"requested_at"`
	Message     string     `json:"message"`
	TimeoutMS   int64      `json:"timeout_ms"`
	OnTimeout   string     `json:"on_timeout"`
	Approver    string     `json:"approver,omitempty"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Resolution  string     `json:"resolution,omitempty"`
	Comment     string     `json:"comment,omitempty"`
}

type Approvals struct {
	Approvals []Approval `json:"approvals"`
}

// ApprovalDecision cannot name an approver; the server derives the actor from authentication.
type ApprovalDecision struct {
	Resolution     string `json:"resolution"`
	Comment        string `json:"comment,omitempty"`
	IdempotencyKey string `json:"-"`
}

type ApprovalResult struct {
	Approval Approval `json:"approval"`
	Replayed bool     `json:"replayed"`
}

// RunReview shares public observations and review mutations across HTTP and api.sock.
// ListApprovals with no run selects pending gates for the authenticated team.
type RunReview interface {
	ReadRun(context.Context, string) (RunDetail, error)
	ReadSteps(context.Context, string) (RunSteps, error)
	ListApprovals(context.Context, string) (Approvals, error)
	ResolveApproval(context.Context, string, string, ApprovalDecision) (ApprovalResult, error)
	AppendAnnotation(context.Context, string, string, AnnotationWrite) error
}
