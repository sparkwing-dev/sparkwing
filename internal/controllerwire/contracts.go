// Package controllerwire owns the fetched-engine/controller HTTP contract.
// Local HTTP and api.sock use these same requests, responses and errors.
package controllerwire

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Compatibility fixes the API boundary independently of an engine's matching node protocol.
// From launch freeze, requests emitted by each release remain accepted through
// its release date plus twelve calendar months. Additive optional fields and
// unknown error codes remain readable. Older releases continue until an explicit
// signed security revocation or minimum policy takes effect, never merely age.
// A new controller cannot reinterpret persisted seals or an older engine's node
// protocol; that engine validates and runs its matching SDK itself.
type Compatibility struct {
	APIMajor        int       `json:"api_major"`
	EngineRelease   string    `json:"engine_release"`
	ReleasedAt      time.Time `json:"released_at"`
	GuaranteedUntil time.Time `json:"guaranteed_until"`
}

const APIMajor = 1

// CompatibilityFor derives the guaranteed boundary from the verified release date
// using limits.ControllerCompatibilityMonths calendar months. It does not reject an
// older release or decide revocation; internal/engine owns that separate policy.
func CompatibilityFor(release engine.VersionInfo) Compatibility {
	return Compatibility{
		APIMajor: APIMajor, EngineRelease: release.Version,
		ReleasedAt:      release.PublishedAt,
		GuaranteedUntil: release.PublishedAt.AddDate(0, limits.ControllerCompatibilityMonths, 0),
	}
}

// CreateRunRequest carries user input; authenticated authority and App facts are server-derived.
type CreateRunRequest struct {
	Request        runspec.Request `json:"request"`
	IdempotencyKey string          `json:"idempotency_key"`
}

type CreateRunResponse struct {
	RunID      string                `json:"run_id"`
	Number     int64                 `json:"number"`
	Request    runspec.SealedRequest `json:"request"`
	HereBearer string                `json:"here_bearer,omitempty"`
	Replayed   bool                  `json:"replayed"`
}

// PlanSubmission has no client-selected acceptance timestamp, actor or authority.
// Declarations preserve restricted pipeline/default checkout layers with origins.
// Resolved settings are proposals; AcceptPlan reruns C3 validation and C4 resolution against
// the stored first seal and operator ceilings before committing the second seal.
type PlanSubmission struct {
	Declarations runspec.CheckoutDeclarations `json:"declarations"`
	Resolved     runspec.Resolved             `json:"resolved"`
	Plan         json.RawMessage              `json:"plan"`
}

type PlanAccepted struct {
	Spec     runspec.Spec    `json:"spec"`
	Plan     json.RawMessage `json:"plan"`
	PlanHash runspec.Hash    `json:"plan_hash"`
	Replayed bool            `json:"replayed"`
	Notices  []engine.Notice `json:"notices"`
}

// ClaimRequest carries self-asserted labels and available capacity only. Server
// registration supplies granted identity, pools, repo authorization and billing.
// RunnerVersion is checked against the signed software policy before a new award;
// it is independent of the pipeline SDK pin. Existing renewals remain permitted to drain.
// RunID/JobID select named claims; omission selects the same ordered queue scan.
type ClaimRequest struct {
	RunnerVersion        string                `json:"runner_version"`
	Kinds                []capability.UnitKind `json:"kinds"`
	Labels               []string              `json:"labels"`
	AvailableCores       float64               `json:"available_cores"`
	AvailableMemoryBytes int64                 `json:"available_memory_bytes"`
	RunID                string                `json:"run_id,omitempty"`
	JobID                string                `json:"job_id,omitempty"`
}

// ClaimedUnit delivers the award once, over authenticated transport. It must never
// be logged or copied into environment variables, public run detail or durable seals.
type ClaimedUnit struct {
	Unit           capability.UnitKind           `json:"unit"`
	UnitID         string                        `json:"unit_id"`
	RunID          string                        `json:"run_id"`
	JobID          string                        `json:"job_id,omitempty"`
	Binding        capability.Binding            `json:"binding"`
	Capability     string                        `json:"capability"`
	Runner         nodeproto.RunnerIdentity      `json:"runner"`
	Request        runspec.SealedRequest         `json:"request"`
	Accepted       *PlanAccepted                 `json:"accepted,omitempty"`
	Execution      *runspec.JobExecutionSettings `json:"execution,omitempty"`
	LeaseExpiresAt time.Time                     `json:"lease_expires_at"`
}

type ClaimResponse struct {
	Claim *ClaimedUnit `json:"claim,omitempty"`
}

// RenewalRequest reports activity, never a replacement holder or generation.
type RenewalRequest struct {
	Executing bool `json:"executing"`
}

// RenewalResponse is shared by trigger, job, plan and eval keepers. Only a
// successful response extends liveness; 409 lock_held, 401 and 403 lose authority.
type RenewalResponse struct {
	ExpiresAt       time.Time `json:"expires_at"`
	CancelRequested bool      `json:"cancel_requested"`
	CancelReason    string    `json:"cancel_reason,omitempty"`
	Watched         bool      `json:"watched"`
	WatchedJobs     []string  `json:"watched_jobs"`
}

// JobResult references authorized records, so a raw digest cannot grant reachability.
type JobResult struct {
	Outcome  string          `json:"outcome"`
	Reason   string          `json:"reason,omitempty"`
	Error    *apierr.Error   `json:"error,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
	Output   *blob.RecordKey `json:"output,omitempty"`
	Manifest *blob.RecordKey `json:"manifest,omitempty"`
}

// JobTransitionRequest is the single report/transition body. The adapter derives
// actor and claim fences from C2; the store derives events and its clock.
type JobTransitionRequest struct {
	To      string     `json:"to"`
	Cause   string     `json:"cause"`
	Result  *JobResult `json:"result,omitempty"`
	RetryAt *time.Time `json:"retry_at,omitempty"`
}

type Applied struct {
	Replayed        bool  `json:"replayed"`
	CancelRequested bool  `json:"cancel_requested"`
	StateSeq        int64 `json:"state_seq"`
	Generation      int64 `json:"generation"`
	Attempt         int64 `json:"attempt"`
}

// ExpansionSubmission requires exactly one Body or Failure. A successful empty
// expansion is {"jobs":[]}. No authority is accepted from this body. Failure
// uses apierr.DecodeInner and enters the same store.AcceptExpansion transaction.
type ExpansionSubmission struct {
	SourceGeneration int64           `json:"source_generation"`
	Body             json.RawMessage `json:"body,omitempty"`
	Failure          *apierr.Error   `json:"failure,omitempty"`
}

type ExpandedJob struct {
	Job       nodeproto.JobSpec            `json:"job"`
	SpecHash  string                       `json:"spec_hash"`
	Execution runspec.JobExecutionSettings `json:"execution"`
}

type ExpansionAccepted struct {
	SourceGeneration int64         `json:"source_generation"`
	State            string        `json:"state"`
	BodyHash         string        `json:"body_hash"`
	Jobs             []ExpandedJob `json:"jobs"`
	Replayed         bool          `json:"replayed"`
}

// ReuseSource proposes a persisted source; its outcome is never caller-supplied.
type ReuseSource struct {
	Policy  string                  `json:"policy"`
	Attempt runspec.AttemptIdentity `json:"attempt"`
	Record  blob.RecordKey          `json:"record"`
}

type ReconcileRequest struct {
	Policy string       `json:"policy"`
	Source *ReuseSource `json:"source,omitempty"`
}

type ReconcileResponse struct {
	Kind   string       `json:"kind"`
	Job    JobDetail    `json:"job"`
	Claim  *ClaimedUnit `json:"claim,omitempty"`
	Source *ReuseSource `json:"source,omitempty"`
}

type FinishRequest struct {
	Outcome string        `json:"outcome"`
	Reason  string        `json:"reason,omitempty"`
	Error   *apierr.Error `json:"error,omitempty"`
}

type CancelRequest struct {
	Reason string `json:"reason"`
}
type RetryRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// JobDetail is a safe status view, not execution authority or a raw job declaration.
type JobDetail struct {
	ID                string     `json:"id"`
	Status            string     `json:"status"`
	Outcome           string     `json:"outcome,omitempty"`
	StatusDetail      string     `json:"status_detail,omitempty"`
	Generation        int64      `json:"generation"`
	Attempt           int64      `json:"attempt"`
	StateSeq          int64      `json:"state_seq"`
	Expansion         string     `json:"expansion"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
}

type AttemptDetail struct {
	JobID      string     `json:"job_id"`
	Attempt    int64      `json:"attempt"`
	Generation int64      `json:"generation"`
	Outcome    string     `json:"outcome,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Executor   string     `json:"executor"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// RunDetail is the shared dashboard, local and remote status result. Inputs must
// come from runspec.DisplayRequest/DisplaySpec, never serialized execution seals.
type RunDetail struct {
	ID                string            `json:"id"`
	Number            int64             `json:"number"`
	Team              string            `json:"team"`
	Status            string            `json:"status"`
	StateSeq          int64             `json:"state_seq"`
	Inputs            runspec.Display   `json:"inputs"`
	Jobs              []JobDetail       `json:"jobs"`
	Attempts          []AttemptDetail   `json:"attempts"`
	Logs              []blob.LogSubject `json:"logs"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
	FinishedAt        *time.Time        `json:"finished_at,omitempty"`
	CancelRequestedAt *time.Time        `json:"cancel_requested_at,omitempty"`
	Engine            *engine.Identity  `json:"engine,omitempty"`
	Notices           []engine.Notice   `json:"notices"`
}

// ScheduleSnapshot transports a consistent read for the remote owner. Graph
// declarations include empty generators. The store adapter projects it through
// the same RunSnapshot.Scheduling path used by controller settlement.
type ScheduleSnapshot struct {
	RunID           string              `json:"run_id"`
	StoreNow        time.Time           `json:"store_now"`
	CancelRequested bool                `json:"cancel_requested"`
	Jobs            []ScheduleJob       `json:"jobs"`
	Generators      []ScheduleGenerator `json:"generators"`
}

type ScheduleJob struct {
	JobDetail
	Declaration nodeproto.JobSpec `json:"declaration"`
	Ready       bool              `json:"ready"`
	Claimed     bool              `json:"claimed"`
}

type ScheduleGenerator struct {
	ID        string   `json:"id"`
	SourceJob string   `json:"source_job"`
	State     string   `json:"state"`
	Members   []string `json:"members"`
}

// Controller is implemented by the shared authenticated HTTP client. Request
// retries use capability.Routes metadata; errors unwrap once via apierr.DecodeInner.
// Identical lifecycle payloads replay. Clients must not retry arbitrary failures.
type Controller interface {
	Compatibility(context.Context, string) (Compatibility, error)
	CreateRun(context.Context, CreateRunRequest) (CreateRunResponse, error)
	ReadRun(context.Context, string) (RunDetail, error)
	ReadSnapshot(context.Context, string) (ScheduleSnapshot, error)
	AcceptPlan(context.Context, string, PlanSubmission) (PlanAccepted, error)
	Claim(context.Context, ClaimRequest) (ClaimResponse, error)
	Renew(context.Context, string, RenewalRequest) (RenewalResponse, error)
	TransitionJob(context.Context, string, string, JobTransitionRequest) (Applied, error)
	ReconcileJob(context.Context, string, string, ReconcileRequest) (ReconcileResponse, error)
	AcceptExpansion(context.Context, string, string, ExpansionSubmission) (ExpansionAccepted, error)
	Finish(context.Context, string, FinishRequest) (Applied, error)
	Cancel(context.Context, string, CancelRequest) (Applied, error)
	Retry(context.Context, string, RetryRequest) (CreateRunResponse, error)
}
