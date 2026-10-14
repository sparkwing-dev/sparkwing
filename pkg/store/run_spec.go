package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// IntakeRequest carries client input and the authenticated caller reference, never self-certified App facts.
type IntakeRequest struct {
	Request            runspec.Request
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
type ExpansionSubmission struct {
	Source           NodeRef
	SourceGeneration int64
	Actor            Actor
	Body             json.RawMessage
	Failure          *ExpansionFailure
}

// ExpansionFailure resolves the barrier without inventing generated topology on a generator error.
type ExpansionFailure struct {
	Code        string
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
// Retry intake loads Lineage.RetryOf from this Tenant, carries its seals unchanged and records the new ID outside them.
func (t *Tenant) AdmitRun(ctx context.Context, request IntakeRequest) (AdmissionResult, error) {
	panic("unimplemented: c1-data")
}

// AcceptPlan validates the C3 plan, ceilings and declared secrets, then seals spec and inserts nodes atomically.
// Neither a request field nor an existing seal may change; a differing sealed hash returns spec_drift.
// Published plans require live publisher authority and create importable jobs without awarding an executable claim.
func (t *Tenant) AcceptPlan(ctx context.Context, request PlanAcceptance) (AcceptedPlan, error) {
	panic("unimplemented: c1-data")
}

// AcceptExpansion checks live owner or eval authority, terminal source generation, cancellation and the barrier in tx.
// A differing repeat is illegal_transition; failed sources resolve to none in their terminal transition instead.
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
