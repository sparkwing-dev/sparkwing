package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
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
	var result AcceptedExpansion
	if (len(request.Body) == 0) == (request.Failure == nil) {
		return result, authorityError(apierr.BadRequest, "expansion requires exactly one body or failure")
	}
	if request.Failure != nil && (request.Failure.MaskedError == "" || len(request.Failure.MaskedError) > int(limits.ErrorBytes) || !slices.ContainsFunc(apierr.Codes, func(code apierr.Definition) bool { return code.Code == request.Failure.Code })) {
		return result, authorityError(apierr.BadRequest, "invalid expansion failure")
	}
	if request.Failure == nil {
		panic("unimplemented: c1-data")
	}
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		now := tx.now()
		if err := expansionAuthorityTx(ctx, tx, t.team, request, now); err != nil {
			return err
		}
		if err := lockStateRunTx(ctx, tx, t.team, request.Source.RunID); err != nil {
			return err
		}
		snapshot, err := readSnapshotTx(ctx, tx, t.team, request.Source.RunID)
		if err != nil {
			return err
		}
		if snapshot.Run.Spec == nil || snapshot.Run.CancelRequestedAt != nil || snapshot.Run.State != RunRunning && snapshot.Run.State != RunPending {
			return authorityError(apierr.IllegalTransition, "expansion run is no longer executable")
		}
		if _, err := snapshot.Scheduling(); err != nil {
			return err
		}
		source, err := readNodeTx(ctx, tx, t.team, request.Source)
		if err != nil {
			return err
		}
		if source.Status != StatusDone || !scheduleSucceeded(string(source.Outcome)) || source.Generation != request.SourceGeneration {
			return holderRefusal()
		}
		result = AcceptedExpansion{Source: request.Source, SourceGeneration: request.SourceGeneration}
		result.State = ExpansionFailed
		failure, err := json.Marshal(request.Failure)
		if err != nil {
			return err
		}
		result.BodyHash, err = stateHash(request.Failure)
		if err != nil {
			return err
		}
		if source.Expansion == result.State && source.ExpansionDigest == result.BodyHash {
			result.Replayed = true
			return nil
		}
		if source.Expansion != ExpansionExpected {
			return authorityError(apierr.IllegalTransition, "expansion differs from its accepted submission")
		}
		write, err := tx.ExecContext(ctx, `UPDATE nodes SET expansion=?,expansion_digest=?,expansion_body=?,expansion_failure=? WHERE team=? AND run_id=? AND node_id=? AND generation=? AND expansion='expected'`, result.State, result.BodyHash, nil, failure, t.team, request.Source.RunID, request.Source.NodeID, request.SourceGeneration)
		if err != nil {
			return err
		}
		if count, err := write.RowsAffected(); err != nil {
			return err
		} else if count != 1 {
			return holderRefusal()
		}
		return settleControllerTx(ctx, tx, t.team, request.Source.RunID, now)
	})
	return result, err
}

func expansionAuthorityTx(ctx context.Context, tx *storeTx, team Team, request ExpansionSubmission, now time.Time) error {
	preliminary, err := authorityChainTx(ctx, tx, request.Actor.Authority, false)
	if err != nil {
		return err
	}
	lease := preliminary[0].principal.Capability.Binding.Expansion
	if lease == nil {
		return holderRefusal()
	}
	var controllerGeneration int64
	if lease.Kind == capability.UnitEval {
		var until int64
		if err := tx.QueryRowContext(ctx, `SELECT generation,until FROM leases WHERE name='controller.owner'`+tx.forUpdate()).Scan(&controllerGeneration, &until); err != nil {
			return err
		}
		if until <= now.UnixNano() {
			return holderRefusal()
		}
	}
	chain, err := authorityChainTx(ctx, tx, request.Actor.Authority, true)
	if err != nil {
		return err
	}
	c := chain[0].principal.Capability
	if c.Team != string(team) || c.Prefix != request.Actor.CapabilityPrefix || c.Audience != capability.Controller || !slices.Contains(c.Grants, capability.ExpansionSubmit) || c.Binding.Kind != capability.BindingNode || c.Binding.RunID != request.Source.RunID || c.Binding.NodeID != request.Source.NodeID || c.Binding.Generation != request.SourceGeneration {
		return holderRefusal()
	}
	lease = c.Binding.Expansion
	if lease == nil {
		return holderRefusal()
	}
	var holder, prefix, status string
	var generation, ownerGeneration int64
	var until, cancelled sql.NullInt64
	switch lease.Kind {
	case capability.UnitTrigger:
		if lease.ID != request.Source.RunID {
			return holderRefusal()
		}
		err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(claimed_by,''),capability_prefix,generation,lease_expires_at,cancel_requested_at FROM triggers WHERE team=? AND id=? AND unit_kind='trigger'`+tx.forUpdate(), team, lease.ID).Scan(&status, &holder, &prefix, &generation, &until, &cancelled)
		if err == nil && !slices.ContainsFunc(chain, func(node authorityNode) bool { return node.principal.Capability.Prefix == prefix }) {
			return holderRefusal()
		}
	case capability.UnitEval:
		unit, decodeErr := decodeAwardUnit(lease.Kind, lease.ID)
		if decodeErr != nil || unit.RunID != request.Source.RunID || unit.NodeID != request.Source.NodeID || unit.SourceGeneration != request.SourceGeneration {
			return holderRefusal()
		}
		err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(claimed_by,''),capability_prefix,generation,lease_expires_at,owner_generation FROM eval_units WHERE team=? AND run_id=? AND source_node_id=? AND source_generation=?`+tx.forUpdate(), team, request.Source.RunID, request.Source.NodeID, request.SourceGeneration).Scan(&status, &holder, &prefix, &generation, &until, &ownerGeneration)
		if err == nil && (prefix != c.Prefix || ownerGeneration != controllerGeneration) {
			return holderRefusal()
		}
	default:
		return holderRefusal()
	}
	if err != nil {
		return err
	}
	if status != "claimed" && status != "running" || holder != lease.Holder || generation != lease.Generation || !until.Valid || until.Int64 <= now.UnixNano() || cancelled.Valid {
		return holderRefusal()
	}
	return nil
}

// safety: intake and both seals own every write-once request/spec projection so retry cannot replace trusted facts.
func admitRun(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, now time.Time) (AdmissionResult, error) {
	panic("unimplemented: c1-data")
}

// safety: the second seal and plan insertion commit together after C3 validation and server-side preflight.
func sealRunSpecTx(ctx context.Context, tx *storeTx, team Team, request PlanAcceptance, now time.Time) (AcceptedPlan, error) {
	panic("unimplemented: c1-data")
}
