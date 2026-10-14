package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// IntakeRequest carries client input and server-derived tagged authority, re-resolved
// by C2 in the intake transaction. CapabilityPrefix is only a claim fence and
// cannot replace a session parent. Verified App facts come from the delivery ID.
type IntakeRequest struct {
	Authority          capability.AuthorityRef
	Request            runspec.Request
	IdempotencyKey     string
	CapabilityPrefix   string
	VerifiedDeliveryID string
}

// AdmissionResult includes the atomically allocated team number and the first sealed request.
type AdmissionResult struct {
	RunID                string
	Number               int64
	Request              runspec.SealedRequest
	OwnerUnit            *OwnerLeaseRef
	HereCapabilityPrefix string
	// HereBearer reaches only the requesting host; C2 owns its encoding and it never enters persisted run JSON.
	HereBearer []byte `json:"-"`
	Replayed   bool
}

// PlanAcceptance binds the second seal and C3 plan to a live owner, or a live run-bound publisher for publish intake.
type PlanAcceptance struct {
	RunID string
	Actor Actor
	Seal  runspec.SealInput
}

// AcceptedPlan is immutable after its first commit; an identical resubmission is replayed.
type AcceptedPlan struct {
	Spec     runspec.Spec
	Plan     json.RawMessage
	PlanHash string
	Owner    *OwnerLeaseRef
	Replayed bool
}

// ExpansionSubmission uses owner or eval authority after the source job's claim has ended.
// Exactly one of a nonempty Body or Failure is required. An empty successful
// expansion is an explicit jobs array, never an omitted or null body.
type ExpansionSubmission struct {
	Source           NodeRef
	SourceGeneration int64
	Actor            Actor
	Body             json.RawMessage
	Failure          *ExpansionFailure
}

// ExpansionFailure resolves the barrier without inventing generated topology on a generator error.
type ExpansionFailure struct {
	Code        apierr.Code
	MaskedError string
}

// AcceptedExpansion reports persisted topology; deterministic code reconstruction must reproduce each SpecHash.
type AcceptedExpansion struct {
	Source           NodeRef
	SourceGeneration int64
	State            ExpansionState
	BodyHash         string
	Nodes            []NodeRow
	Replayed         bool
}

// AdmitRun is the only intake for local, remote, retry, child and published runs.
// It derives authority and allowed pools in the transaction; published runs receive no executable owner unit.
// A nonempty idempotency key is scoped by server-derived team, principal and pipeline.
// The canonical input digest (including retry lineage) and key commit on runs for
// executable and published intake alike. Same key/body returns the original result;
// a changed body conflicts. An empty key requests a distinct invocation.
// Retry intake loads Lineage.RetryOf from this Tenant, carries its seals unchanged and records the new ID outside them.
func (t *Tenant) AdmitRun(ctx context.Context, request IntakeRequest) (AdmissionResult, error) {
	panic("unimplemented: c1-data")
}

// AcceptPlan validates C3 plan and restricted checkout declarations, then resolves
// C4 settings from the stored first seal, ceilings, both checkout layers and code.
// Seal.Declarations carries both layers; resolved proposals must match
// the independently resolved winners. It commits Spec.Declarations with the second
// seal so expansion and retry never reconstruct layers from winners or current YAML.
// Declared secret preflight and node insertion share that same transaction.
// Neither a request field nor an existing seal may change; a differing sealed hash returns spec_drift.
// Published plans require live publisher authority and create importable jobs without awarding an executable claim.
func (t *Tenant) AcceptPlan(ctx context.Context, request PlanAcceptance) (AcceptedPlan, error) {
	panic("unimplemented: c1-data")
}

// AcceptExpansion is the single writer for successful and failed generator evaluation.
// It checks Body XOR Failure, live owner/eval authority, terminal source generation,
// cancellation and the barrier in tx. Success validates declarations, resolves C4
// execution settings and persists both beside spec hashes before inserting jobs.
// Structured failures persist their code and masked error. Both forms hash their
// canonical submission and replay identically; a differing repeat is illegal_transition.
// Failed source jobs resolve to none in their terminal transition instead.
func (t *Tenant) AcceptExpansion(ctx context.Context, request ExpansionSubmission) (AcceptedExpansion, error) {
	panic("unimplemented: c1-data")
}

// safety: intake and both seals own every write-once request/spec projection so retry cannot replace trusted facts.
func admitRun(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, now time.Time) (AdmissionResult, error) {
	panic("unimplemented: c1-data")
}

// safety: the second seal and plan insertion commit together after C3 validation and server-side preflight.
func sealRunSpecTx(ctx context.Context, tx *storeTx, team Team, request PlanAcceptance, now time.Time) (AcceptedPlan, error) {
	panic("unimplemented: c1-data")
}
