package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// IntakeRequest carries client input and server-derived tagged authority, re-resolved
// by C2 in the intake transaction. CapabilityPrefix is only a claim fence and
// cannot replace a session parent. Verified App facts come from the delivery ID.
// Intent, when set, is Request before server-side source resolution and is the
// idempotency digest input, so replay never depends on Git state.
type IntakeRequest struct {
	SubmittedEnv       map[string][]byte `json:"-"`
	Authority          capability.AuthorityRef
	Request            runspec.Request
	Intent             *runspec.Request
	IdempotencyKey     string
	CapabilityPrefix   string
	LocalSecrets       map[string]string `json:"-"`
	VerifiedDeliveryID string
	RetryMode          string
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
// Verified App redeliveries return the original run for that delivery and pipeline,
// including when the caller supplies no idempotency key.
// Retry intake loads Lineage.RetryOf from this Tenant, carries its seals unchanged and records the new ID outside them.
func (t *Tenant) AdmitRun(ctx context.Context, request IntakeRequest) (AdmissionResult, error) {
	var result AdmissionResult
	var installed *localRunSecrets
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = admitRun(ctx, tx, t.team, request, tx.now())
		if err == nil {
			installed, err = t.validateAdmissionInputsTx(ctx, tx, result, request)
		}
		return err
	})
	if err != nil {
		if installed != nil {
			t.s.localSecrets.CompareAndDelete(localSecretKey{t.team, result.RunID}, installed)
		}
		return AdmissionResult{}, err
	}
	return result, nil
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
	var result AcceptedPlan
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		var err error
		result, err = sealRunSpecTx(ctx, tx, t.team, request, tx.now())
		if err != nil {
			return err
		}
		return persistPlanResultProofTx(ctx, tx, t.team, request)
	})
	if err != nil {
		var refused *SecretPreflightError
		if errors.As(err, &refused) {
			return AcceptedPlan{}, errors.Join(err, t.s.recordSecretPreflight(ctx, t.team, request, refused))
		}
		return AcceptedPlan{}, err
	}
	return result, nil
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
		accepted, err := prepareExpansionTx(ctx, tx, t.team, request, snapshot)
		if err != nil {
			return err
		}
		result.State, result.BodyHash = accepted.state, accepted.hash
		if source.Expansion == result.State && source.ExpansionDigest == result.BodyHash {
			result.Nodes, err = acceptedExpansionNodes(snapshot, accepted.plan)
			if err != nil {
				return err
			}
			if err := expansionAuthorityTx(ctx, tx, t.team, request, tx.now()); err != nil {
				return err
			}
			result.Replayed = true
			return nil
		}
		if source.Expansion != ExpansionExpected {
			return authorityError(apierr.IllegalTransition, "expansion differs from its accepted submission")
		}
		if request.Failure == nil {
			result.Nodes, err = insertExpansionTx(ctx, tx, t.team, request, snapshot, accepted)
			if err != nil {
				return err
			}
		}
		if err := expansionAuthorityTx(ctx, tx, t.team, request, tx.now()); err != nil {
			return err
		}
		write, err := tx.ExecContext(ctx, `UPDATE nodes SET expansion=?,expansion_digest=?,expansion_body=?,expansion_failure=? WHERE team=? AND run_id=? AND node_id=? AND generation=? AND expansion='expected'`, result.State, result.BodyHash, accepted.body, accepted.failure, t.team, request.Source.RunID, request.Source.NodeID, request.SourceGeneration)
		if err != nil {
			return err
		}
		if count, err := write.RowsAffected(); err != nil {
			return err
		} else if count != 1 {
			return holderRefusal()
		}
		if err := finishExpansionBillingTx(ctx, tx, t.team, request); err != nil {
			return err
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
		if err := tx.QueryRowContext(ctx, `SELECT generation,until FROM leases WHERE name='controller.owner'`+tx.forKeyShare()).Scan(&controllerGeneration, &until); err != nil {
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

func resolveReapedExpansionTx(ctx context.Context, tx *storeTx, target expiredUnit, status string, actor Actor, now time.Time) error {
	if err := checkReaperTx(ctx, tx, actor, now); err != nil {
		return err
	}
	return resolveExpansionLossTx(ctx, tx, target, status, now)
}

func resolveExpansionLossTx(ctx context.Context, tx *storeTx, target expiredUnit, status string, now time.Time) error {
	state := ExpansionFailed
	failure := &ExpansionFailure{Code: apierr.Unavailable, MaskedError: "generator evaluator lost"}
	if status == "cancelled" {
		state, failure = ExpansionNone, nil
	}
	body, err := json.Marshal(failure)
	if err != nil {
		return err
	}
	digest, err := stateHash(failure)
	if err != nil {
		return err
	}
	write, err := tx.ExecContext(ctx, `UPDATE nodes SET expansion=?,expansion_failure=?,expansion_digest=? WHERE team=? AND run_id=? AND node_id=? AND generation=? AND expansion='expected'`, state, body, digest, target.team, target.ref.RunID, target.ref.NodeID, target.sourceGeneration)
	if err != nil {
		return err
	}
	count, err := write.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return holderRefusal()
	}
	return settleControllerTx(ctx, tx, target.team, target.ref.RunID, now)
}

func insertAdmissionTx(ctx context.Context, tx *storeTx, team Team, result AdmissionResult, snapshot admittedSnapshot, p capability.Principal, key, scope, digest string, policy IntakePolicyFacts, now time.Time) error {
	input := snapshot.request.Request
	body, err := json.Marshal(snapshot.request)
	if err != nil {
		return err
	}
	pools, err := json.Marshal(snapshot.request.AllowedPools)
	if err != nil {
		return err
	}
	lineage, err := json.Marshal(executionLineage{Lineage: snapshot.lineage, Retry: snapshot.retry})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,idempotency_scope,idempotency_key,idempotency_digest,request_sealed_at,trigger_source,git_branch,git_sha,repo_key,repo_display,retry_of,lineage_json,parent_run_id,parent_node_id,dispatch,intake_mode,admission,allowed_pools,created_subject_kind,created_principal,created_at,claim_wait_ns,log_retention_class,log_retention_days) VALUES (?,?,?,?,'pending',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, team, result.RunID, result.Number, input.Pipeline, body, snapshot.request.Hash, scope, key, digest, snapshot.request.AdmittedAt.UnixNano(), input.Origin, input.Git.Ref, input.Git.SHA, input.Git.Repository.Key, input.Git.Repository.Display, snapshot.lineage.RetryOf, lineage, snapshot.lineage.ParentRunID, snapshot.lineage.ParentJobID, input.Dispatch, input.Mode, input.Options.AdmissionClass.Value, pools, p.Capability.Subject.Kind, p.Capability.Subject.ID, now.UnixNano(), int64(input.Options.ClaimWait.Value), policy.LogRetentionClass, policy.LogRetentionDays)
	if err != nil {
		return err
	}
	if input.Mode == runspec.IntakePublish {
		return nil
	}
	kind := capability.UnitTrigger
	if input.Dispatch == runspec.DispatchController {
		kind = capability.UnitPlan
	}
	needs, err := json.Marshal(input.Options.Requires.Value)
	if err != nil {
		return err
	}
	prefers, err := json.Marshal(input.Options.Prefers.Value)
	if err != nil {
		return err
	}
	var expires any
	if input.Options.OwnerExecutionTimeout.Set && input.Options.OwnerExecutionTimeout.Value > 0 {
		expires = now.Add(input.Options.OwnerExecutionTimeout.Value).UnixNano()
	}
	delivery, replayKey := input.Git.App.Value.DeliveryKey, appReplayKey(input)
	if snapshot.lineage.RetryOf != "" {
		delivery, replayKey = "", ""
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,expires_at,needs_labels,prefers_labels,webhook_delivery,webhook_replay_key) VALUES (?,?,'pending',?,?,?,?,?,?,?,?)`, team, result.RunID, kind, now.UnixNano(), now.UnixNano(), expires, needs, prefers, delivery, replayKey)
	return err
}

func writeAcceptedPlanTx(ctx context.Context, tx *storeTx, team Team, id string, spec runspec.Spec, body json.RawMessage, generation int64) error {
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(body, &plan); err != nil {
		return err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	write, err := tx.ExecContext(ctx, `UPDATE runs SET spec_json=?,spec_hash=?,sealed_at=?,plan_json=?,plan_hash=?,plan_accepted_generation=?,claim_wait_ns=?,admission=? WHERE team=? AND id=? AND sealed_at IS NULL`, raw, spec.Hash, spec.SealedAt.UnixNano(), []byte(body), spec.PlanHash, generation, int64(spec.Resolved.Settings.Options.ClaimWait.Value), spec.Resolved.Settings.Options.AdmissionClass.Value, team, id)
	if err != nil {
		return err
	}
	count, err := write.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return authorityError(apierr.SpecDrift, "run already has a sealed plan")
	}
	for _, job := range plan.Jobs {
		execution, ok := spec.Resolved.Settings.Jobs[job.ID]
		if !ok {
			return authorityError(apierr.PlanInvalid, "job has no resolved execution settings")
		}
		raw, err := json.Marshal(job)
		if err != nil {
			return err
		}
		hash, err := tx.store.planServices().CanonicalHash(job)
		if err != nil {
			return err
		}
		expansion := ExpansionAbsent
		if len(job.Closures.Generate) > 0 {
			expansion = ExpansionExpected
		}
		if err = insertNodeTx(ctx, tx, team, NodeRow{Ref: NodeRef{id, job.ID}, Status: StatusPending, Spec: raw, SpecHash: string(hash), Execution: execution, Expansion: expansion}); err != nil {
			return err
		}
	}
	snapshot, err := readSnapshotTx(ctx, tx, team, id)
	if err != nil {
		return err
	}
	_, err = snapshot.Scheduling()
	return err
}

func sealRunSpecTx(ctx context.Context, tx *storeTx, team Team, request PlanAcceptance, now time.Time) (AcceptedPlan, error) {
	var controller *Actor
	var dispatch string
	if err := tx.QueryRowContext(ctx, `SELECT dispatch FROM runs WHERE team=? AND id=?`, team, request.RunID).Scan(&dispatch); err != nil {
		return AcceptedPlan{}, err
	}
	if dispatch == string(runspec.DispatchController) {
		var holder string
		var generation, until int64
		err := tx.QueryRowContext(ctx, `SELECT holder,generation,until FROM leases WHERE name='controller.owner'`+tx.forKeyShare()).Scan(&holder, &generation, &until)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return AcceptedPlan{}, err
		}
		if err == nil && until > now.UnixNano() {
			controller = &Actor{Kind: ActorController, Owner: OwnerLeaseRef{Kind: OwnerController, Name: "controller.owner", RunID: request.RunID, Holder: holder, Generation: generation}}
		}
	}
	if err := planAuthorityTx(ctx, tx, team, request); err != nil {
		return AcceptedPlan{}, err
	}
	if err := lockStateRunTx(ctx, tx, team, request.RunID); err != nil {
		return AcceptedPlan{}, err
	}
	run, err := readRunTx(ctx, tx, team, request.RunID)
	if err != nil {
		return AcceptedPlan{}, err
	}
	if run.Spec == nil && (run.CancelRequestedAt != nil || run.FinishedAt != nil) {
		return AcceptedPlan{}, authorityError(apierr.IllegalTransition, "run is no longer accepting plans")
	}
	spec, body, err := preparePlanSealTx(ctx, tx, team, request, run)
	if err != nil {
		return AcceptedPlan{}, err
	}
	replay := run.Spec != nil
	if replay && run.Spec.Hash != spec.Hash {
		return AcceptedPlan{}, authorityError(apierr.SpecDrift, "plan differs from the accepted seal")
	}
	if err = planAuthorityTx(ctx, tx, team, request); err != nil {
		return AcceptedPlan{}, err
	}
	if replay && (run.FinishedAt != nil || request.Actor.Result != nil) {
		return AcceptedPlan{Spec: *run.Spec, Plan: run.Plan, PlanHash: run.PlanHash, Owner: run.Owner, Replayed: true}, nil
	}
	if run.Request.Request.Mode == runspec.IntakeExecute && dispatch == string(runspec.DispatchController) && controller == nil {
		return AcceptedPlan{}, authorityError(apierr.Unavailable, "controller ownership lease is unavailable")
	}
	if !replay {
		if err = writeAcceptedPlanTx(ctx, tx, team, run.ID, spec, body, request.Actor.Owner.Generation); err != nil {
			return AcceptedPlan{}, err
		}
	} else {
		spec, body = *run.Spec, run.Plan
	}
	if err = planAuthorityTx(ctx, tx, team, request); err != nil {
		return AcceptedPlan{}, err
	}
	if run.Request.Request.Mode == runspec.IntakeExecute && dispatch == string(runspec.DispatchController) {
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET plan_accepted_generation=? WHERE team=? AND id=? AND plan_accepted_generation=0`, request.Actor.Owner.Generation, team, run.ID); err != nil {
			return AcceptedPlan{}, err
		}
		if err = acceptControllerPlanTx(ctx, tx, team, request, *controller, replay); err != nil {
			return AcceptedPlan{}, err
		}
		if run.State == RunPending {
			if _, err = transitionRunTx(ctx, tx, team, RunTransition{RunID: run.ID, To: RunRunning, Actor: *controller, Cause: RunStart}, tx.now()); err != nil {
				return AcceptedPlan{}, err
			}
		}
		if err = settleControllerTx(ctx, tx, team, run.ID, tx.now()); err != nil {
			return AcceptedPlan{}, err
		}
		return AcceptedPlan{Spec: spec, Plan: body, PlanHash: string(spec.PlanHash), Owner: &controller.Owner, Replayed: replay}, nil
	}
	return AcceptedPlan{Spec: spec, Plan: body, PlanHash: string(spec.PlanHash), Owner: run.Owner, Replayed: replay}, nil
}
