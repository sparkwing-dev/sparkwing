// Package controllerwire owns the fetched-engine/controller HTTP contract.
package controllerwire

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// Compatibility fixes the API boundary independently of an engine's matching node protocol.
type Compatibility struct {
	APIMajor        int       `json:"api_major"`
	EngineRelease   string    `json:"engine_release"`
	ReleasedAt      time.Time `json:"released_at"`
	GuaranteedUntil time.Time `json:"guaranteed_until"`
}

const APIMajor = 1

// CompatibilityFor derives the API guarantee using calendar months from the verified release date.
func CompatibilityFor(release engineid.VersionInfo) Compatibility {
	return Compatibility{
		APIMajor: APIMajor, EngineRelease: release.Version,
		ReleasedAt:      release.PublishedAt,
		GuaranteedUntil: release.PublishedAt.AddDate(0, limits.ControllerCompatibilityMonths, 0),
	}
}

// CreateRunRequest carries user input; authenticated authority and App facts are server-derived.
type CreateRunRequest struct {
	SubmittedEnv   map[string][]byte `json:"submitted_env,omitempty"`
	Request        runspec.Request   `json:"request"`
	IdempotencyKey string            `json:"idempotency_key"`
	LocalSecrets   map[string]string `json:"local_secrets,omitempty"`
	// OriginMachine is a short-lived proof that the submitter holds this machine's runner credential, bound to the
	// request's idempotency key; it never carries the credential itself.
	OriginMachine string `json:"origin_machine,omitempty"`
}

// CreateRunResponse returns the accepted run and any first-intake here bearer.
type CreateRunResponse struct {
	RunID      string          `json:"run_id"`
	Number     int64           `json:"number"`
	Inputs     runspec.Display `json:"inputs"`
	RequestID  string          `json:"request_id,omitempty"`
	WriteScope string          `json:"write_scope"`
	Here       bool            `json:"here"`
	// HereBearer is issued once and retained privately for resume.
	HereBearer string `json:"here_bearer,omitempty"`
	Replayed   bool   `json:"replayed"`
}

// PlanSubmission proposes checkout declarations, resolved settings and a plan for store acceptance.
type PlanSubmission struct {
	Declarations runspec.CheckoutDeclarations `json:"declarations"`
	Resolved     runspec.Resolved             `json:"resolved"`
	Plan         json.RawMessage              `json:"plan"`
}

type PlanAccepted struct {
	Spec     runspec.Spec      `json:"spec"`
	Plan     json.RawMessage   `json:"plan"`
	PlanHash runspec.Hash      `json:"plan_hash"`
	Replayed bool              `json:"replayed"`
	Notices  []engineid.Notice `json:"notices"`
}

// ClaimRequest proposes selectors, labels and free capacity for a server-authorized award.
type ClaimRequest struct {
	FreeSlots          *int                  `json:"free_slots,omitempty"`
	OS                 string                `json:"os,omitempty"`
	Arch               string                `json:"arch,omitempty"`
	RunnerVersion      string                `json:"runner_version"`
	Kinds              []capability.UnitKind `json:"kinds"`
	Labels             []string              `json:"labels"`
	JobNamespace       string                `json:"job_namespace,omitempty"`
	ExcludedTeamLabels []string              `json:"excluded_team_labels,omitempty"`
	RunID              string                `json:"run_id,omitempty"`
	JobID              string                `json:"job_id,omitempty"`
}

// ClaimedUnit carries the one-time private execution award over authenticated transport.
type ClaimedUnit struct {
	SubmittedEnv           map[string][]byte             `json:"submitted_env,omitempty"`
	OwnerMachine           bool                          `json:"owner_machine,omitempty"`
	CloudResources         *match.Resources              `json:"cloud_resources,omitempty"`
	Unit                   capability.UnitKind           `json:"unit"`
	Purpose                capability.AwardPurpose       `json:"purpose"`
	Delivery               capability.DeliveryPurpose    `json:"delivery"`
	UnitID                 string                        `json:"unit_id"`
	RunID                  string                        `json:"run_id"`
	JobID                  string                        `json:"job_id,omitempty"`
	Binding                capability.Binding            `json:"binding"`
	Capability             string                        `json:"capability"`
	Runner                 nodeproto.RunnerIdentity      `json:"runner"`
	Request                runspec.SealedRequest         `json:"request"`
	Accepted               *PlanAccepted                 `json:"accepted,omitempty"`
	Execution              *runspec.JobExecutionSettings `json:"execution,omitempty"`
	LeaseExpiresAt         time.Time                     `json:"lease_expires_at"`
	OwnerDeadline          *time.Time                    `json:"owner_deadline,omitempty"`
	AttemptDeadline        *time.Time                    `json:"attempt_deadline,omitempty"`
	ParentCapabilityExpiry *time.Time                    `json:"parent_capability_expiry,omitempty"`
	SourceGeneration       int64                         `json:"source_generation,omitempty"`
	Engine                 *engineid.Identity            `json:"engine,omitempty"`
}

type ClaimResponse struct {
	Claim *ClaimedUnit `json:"claim,omitempty"`
}

// LauncherCapacityObservation reports demand without an execution placement.
type LauncherCapacityObservation struct {
	Award capability.LeaseRef `json:"award"`
}

// LauncherSyncRequest reconciles Jobs and node reservations against the launcher's awards.
type LauncherSyncRequest struct {
	Observed    []capability.LauncherObservation `json:"observed"`
	Capacity    []LauncherCapacityObservation    `json:"capacity,omitempty"`
	Activations []capability.LauncherActivation  `json:"activations,omitempty"`
	Preparing   []capability.LeaseRef            `json:"preparing,omitempty"`
}

// LauncherPreparation carries a bootstrap fence and its committed lease deadline.
type LauncherPreparation struct {
	capability.LeaseRef
	ExpiresAt time.Time `json:"expires_at"`
}

// LauncherSyncResponse carries execution inputs and retired award fences without bearers.
type LauncherSyncResponse struct {
	Deferred []capability.LeaseRef `json:"deferred,omitempty"`
	Prepared []LauncherPreparation `json:"prepared,omitempty"`
	Awards   []ClaimedUnit         `json:"awards"`
	Retired  []capability.LeaseRef `json:"retired"`
}

// ExchangeResponse returns the original unit with its exchanged in-memory authority once.
type ExchangeResponse struct {
	Claim ClaimedUnit `json:"claim"`
}

// RenewalRequest reports activity, never a replacement holder or generation.
type RenewalRequest struct {
	LocalSecrets map[string]string `json:"local_secrets,omitempty"`
	Executing    bool              `json:"executing"`
}

// RenewalResponse carries liveness, cancellation and watch state for every unit keeper.
type RenewalResponse struct {
	ExpiresAt       time.Time `json:"expires_at"`
	CancelRequested bool      `json:"cancel_requested"`
	CancelReason    string    `json:"cancel_reason,omitempty"`
	Watched         bool      `json:"watched"`
	WatchedJobs     []string  `json:"watched_jobs"`
}

// JobResult references authorized records, so a raw digest cannot grant reachability.
type JobResult struct {
	MaskedError string                `json:"masked_error,omitempty"`
	Outcome     string                `json:"outcome"`
	Reason      string                `json:"reason,omitempty"`
	Failure     *nodeproto.JobFailure `json:"failure,omitempty"`
	ExitCode    *int                  `json:"exit_code,omitempty"`
	Output      *blob.RecordKey       `json:"output,omitempty"`
	Manifest    *blob.RecordKey       `json:"manifest,omitempty"`
}

// JobTransitionRequest carries a transition and the observed source attempt without caller authority.
type JobTransitionRequest struct {
	Publication   *runspec.PublishedAttempt `json:"publication,omitempty"`
	To            string                    `json:"to"`
	Cause         string                    `json:"cause"`
	SourceAttempt *int64                    `json:"source_attempt,omitempty"`
	Result        *JobResult                `json:"result,omitempty"`
	RetryAt       *time.Time                `json:"retry_at,omitempty"`
}

type Applied struct {
	Outcome         string `json:"outcome,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Replayed        bool   `json:"replayed"`
	CancelRequested bool   `json:"cancel_requested"`
	StateSeq        int64  `json:"state_seq"`
	Generation      int64  `json:"generation"`
	Attempt         int64  `json:"attempt"`
}

// ExpansionSubmission carries one expansion body or failure under its source generation.
type ExpansionSubmission struct {
	SourceGeneration int64                      `json:"source_generation"`
	Body             json.RawMessage            `json:"body,omitempty"`
	Registration     runspec.SecretRegistration `json:"registration,omitempty"`
	Failure          *apierr.Error              `json:"failure,omitempty"`
}

type ExpandedJob struct {
	Job       nodeproto.JobSpec            `json:"job"`
	SpecHash  string                       `json:"spec_hash"`
	Execution runspec.JobExecutionSettings `json:"execution"`
	Current   ExecutionJob                 `json:"current"`
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
	Policy  string                    `json:"policy"`
	Attempt runspec.AttemptIdentity   `json:"attempt"`
	Record  blob.RecordKey            `json:"record"`
	Result  *MaterializedResultSource `json:"result,omitempty"`
}

// MaterializedResultSource names a committed terminal result independently of execution attempts.
type MaterializedResultSource struct {
	RunID    string `json:"run_id"`
	JobID    string `json:"job_id"`
	StateSeq int64  `json:"state_seq"`
}

type ReconcileRequest struct {
	Policy          string       `json:"policy"`
	Source          *ReuseSource `json:"source,omitempty"`
	MaterializeOnly bool         `json:"materialize_only,omitempty"`
}

type ReconcileResponse struct {
	Kind   string       `json:"kind"`
	Job    ExecutionJob `json:"job"`
	Claim  *ClaimedUnit `json:"claim,omitempty"`
	Source *ReuseSource `json:"source,omitempty"`
}

// ExecutionJob carries private persisted state for a live owner or awarded executor.
type ExecutionJob struct {
	JobDetail
	OutputRecord          *blob.RecordKey              `json:"output_record,omitempty"`
	ManifestRecord        *blob.RecordKey              `json:"manifest_record,omitempty"`
	Spec                  json.RawMessage              `json:"spec"`
	SpecHash              string                       `json:"spec_hash"`
	Execution             runspec.JobExecutionSettings `json:"execution"`
	ClaimedBy             string                       `json:"claimed_by,omitempty"`
	CapabilityPrefix      string                       `json:"capability_prefix,omitempty"`
	OwnerCapabilityPrefix string                       `json:"owner_capability_prefix,omitempty"`
	ReadyAt               *time.Time                   `json:"ready_at,omitempty"`
	LeaseExpiresAt        *time.Time                   `json:"lease_expires_at,omitempty"`
	ApplicationRetries    int                          `json:"application_retries"`
	Disruptions           int                          `json:"disruptions"`
	ExpansionDigest       string                       `json:"expansion_digest,omitempty"`
}

// AcceptedExpansion retains exact accepted declarations for reconstruction, including empty groups.
type AcceptedExpansion struct {
	SourceJob        string                     `json:"source_job"`
	SourceGeneration int64                      `json:"source_generation"`
	Body             json.RawMessage            `json:"body"`
	BodyHash         string                     `json:"body_hash"`
	Registration     runspec.SecretRegistration `json:"registration,omitempty"`
}

type FinishRequest struct {
	Reason string        `json:"reason,omitempty"`
	Error  *apierr.Error `json:"error,omitempty"`
}

type CancelRequest struct {
	Reason string `json:"reason"`
}
type RetryRequest struct {
	IdempotencyKey string    `json:"idempotency_key"`
	Mode           RetryMode `json:"mode"`
}

// RetryMode selects reuse of successful jobs or execution of the complete sealed plan.
type RetryMode string

const (
	RetryFailed RetryMode = "failed"
	RetryAll    RetryMode = "all"
)

// Validate refuses an ambiguous retry before intake can allocate a new run.
func (r RetryRequest) Validate() error {
	if r.Mode != RetryFailed && r.Mode != RetryAll {
		return &apierr.Error{Code: apierr.BadRequest, Message: "retry requires mode failed or all"}
	}
	if r.IdempotencyKey == "" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "retry requires an idempotency key"}
	}
	return nil
}

// JobDetail is a safe status view, not execution authority or a raw job declaration.
type JobDetail struct {
	Annotations       []Annotation `json:"annotations,omitempty"`
	ID                string       `json:"id"`
	Status            string       `json:"status"`
	Outcome           string       `json:"outcome,omitempty"`
	StatusDetail      string       `json:"status_detail,omitempty"`
	Generation        int64        `json:"generation"`
	Attempt           int64        `json:"attempt"`
	StateSeq          int64        `json:"state_seq"`
	Expansion         string       `json:"expansion"`
	StartedAt         *time.Time   `json:"started_at,omitempty"`
	FinishedAt        *time.Time   `json:"finished_at,omitempty"`
	CancelRequestedAt *time.Time   `json:"cancel_requested_at,omitempty"`
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

// RunDetail carries safe dashboard, local and remote status projections.
type RunDetail struct {
	ID                string             `json:"id"`
	Number            int64              `json:"number"`
	Team              string             `json:"team"`
	Status            string             `json:"status"`
	StateSeq          int64              `json:"state_seq"`
	Inputs            runspec.Display    `json:"inputs"`
	Jobs              []JobDetail        `json:"jobs"`
	Attempts          []AttemptDetail    `json:"attempts"`
	Steps             []StepDetail       `json:"steps,omitempty"`
	Logs              []blob.LogSubject  `json:"logs"`
	StartedAt         *time.Time         `json:"started_at,omitempty"`
	FinishedAt        *time.Time         `json:"finished_at,omitempty"`
	CancelRequestedAt *time.Time         `json:"cancel_requested_at,omitempty"`
	Engine            *engineid.Identity `json:"engine,omitempty"`
	Notices           []engineid.Notice  `json:"notices"`
}

// ScheduleSnapshot carries a consistent scheduling projection for the remote owner.
type ScheduleSnapshot struct {
	RunID           string              `json:"run_id"`
	StoreNow        time.Time           `json:"store_now"`
	CancelRequested bool                `json:"cancel_requested"`
	GuardRefusal    string              `json:"guard_refusal,omitempty"`
	SelectionSkips  map[string]string   `json:"selection_skips,omitempty"`
	Jobs            []ScheduleJob       `json:"jobs"`
	Generators      []ScheduleGenerator `json:"generators"`
	Expansions      []AcceptedExpansion `json:"expansions"`
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

// Controller is the authenticated fetched-engine API shared by every placement.
type Controller interface {
	Compatibility(context.Context, string) (Compatibility, error)
	CreateRun(context.Context, CreateRunRequest) (CreateRunResponse, error)
	ReadRun(context.Context, string) (RunDetail, error)
	ReadSnapshot(context.Context, string) (ScheduleSnapshot, error)
	AcceptPlan(context.Context, string, PlanSubmission) (PlanAccepted, error)
	Claim(context.Context, ClaimRequest) (ClaimResponse, error)
	SyncLauncher(context.Context, LauncherSyncRequest) (LauncherSyncResponse, error)
	Exchange(context.Context) (ExchangeResponse, error)
	Renew(context.Context, string, RenewalRequest) (RenewalResponse, error)
	TransitionJob(context.Context, string, string, JobTransitionRequest) (Applied, error)
	RecordStep(context.Context, string, string, StepReportRequest) error
	ReconcileJob(context.Context, string, string, ReconcileRequest) (ReconcileResponse, error)
	AcceptExpansion(context.Context, string, string, ExpansionSubmission) (ExpansionAccepted, error)
	Finish(context.Context, string, FinishRequest) (Applied, error)
	Cancel(context.Context, string, CancelRequest) (Applied, error)
	CancelJob(context.Context, string, string, CancelRequest) (Applied, error)
	ReadFailure(context.Context, string, string, int64) (*nodeproto.JobFailure, error)
	Retry(context.Context, string, RetryRequest) (CreateRunResponse, error)
}
