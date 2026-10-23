package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type admittedSnapshot struct {
	request   runspec.SealedRequest
	spec      *runspec.Spec
	plan      json.RawMessage
	execution executionLineage
}

// ReplayRun validates private inputs and returns a keyed admission before source resolution.
func (t *Tenant) ReplayRun(ctx context.Context, request IntakeRequest) (AdmissionResult, bool, error) {
	if request.IdempotencyKey == "" || request.Request.Lineage.RetryOf != "" || request.VerifiedDeliveryID != "" {
		return AdmissionResult{}, false, nil
	}
	var result AdmissionResult
	var found bool
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		principal, err := intakeAuthorityTx(ctx, tx, t.team, request)
		if err != nil {
			return err
		}
		digest, err := intakeDigest(request, admittedSnapshot{})
		if err != nil {
			return err
		}
		scope, err := intakeScope(principal, request.Request.Pipeline)
		if err != nil {
			return err
		}
		result, found, err = replayAdmissionTx(ctx, tx, t.team, scope, request.IdempotencyKey, string(digest))
		if err == nil && found {
			_, err = t.validateAdmissionInputsTx(ctx, tx, result, request)
		}
		return err
	})
	if err != nil {
		return AdmissionResult{}, false, err
	}
	return result, found, nil
}

func (t *Tenant) validateAdmissionInputsTx(ctx context.Context, tx *storeTx, result AdmissionResult, request IntakeRequest) (*localRunSecrets, error) {
	if err := validateLocalSecrets(request.Request.LocalSecretNames, request.LocalSecrets); err != nil {
		return nil, err
	}
	installed, err := t.s.installLocalSecrets(t.team, result, request.LocalSecrets)
	if err == nil {
		err = t.installSubmittedEnvironmentTx(ctx, tx, result, request)
	}
	return installed, err
}

func admitRun(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, now time.Time) (AdmissionResult, error) {
	principal, err := intakeAuthorityTx(ctx, tx, team, request)
	if err != nil {
		return AdmissionResult{}, err
	}
	snapshot, err := retryIntakeTx(ctx, tx, team, request, principal)
	if err != nil {
		return AdmissionResult{}, err
	}
	digest, err := intakeDigest(request, snapshot)
	if err != nil {
		return AdmissionResult{}, err
	}
	pipeline := request.Request.Pipeline
	if snapshot.request.RunID != "" {
		pipeline = snapshot.request.Request.Pipeline
	}
	scope, err := intakeScope(principal, pipeline)
	if err != nil {
		return AdmissionResult{}, err
	}

	// safety: serializing intake per team reserves the number without taking the team row before owner authority.
	if err = advisoryLockTx(ctx, tx, "intake:"+string(team)); err != nil {
		return AdmissionResult{}, err
	}
	if request.IdempotencyKey != "" {
		replay, found, err := replayAdmissionTx(ctx, tx, team, scope, request.IdempotencyKey, string(digest))
		if err == nil && found {
			_, err = intakeAuthorityTx(ctx, tx, team, request)
		}
		if err != nil || found {
			return replay, err
		}
	}
	if tx.store.intakePolicy == nil {
		return AdmissionResult{}, authorityError(apierr.Unavailable, "intake policy is not configured")
	}
	if snapshot.execution.Retry != nil {
		if err := selectRetryTx(ctx, tx, team, snapshot.execution.Lineage.RetryOf, snapshot.execution.Retry); err != nil {
			return AdmissionResult{}, err
		}
	}
	policy, err := tx.store.intakePolicy.ResolveIntake(ctx, tx, request)
	if err != nil {
		return AdmissionResult{}, err
	}
	if policy.LogRetentionClass == "" || policy.LogRetentionDays <= 0 {
		return AdmissionResult{}, authorityError(apierr.Unavailable, "intake log retention policy is missing")
	}
	id, err := newCreditID("run")
	if err != nil {
		return AdmissionResult{}, err
	}
	charge := admissionIdentity{AdmissionSubject: principal.Capability.Subject, AdmissionKey: policy.AdmissionKey}
	if policy.AdmissionKey == "team:"+string(team) {
		charge.AdmissionSubject = capability.Subject{Kind: capability.SubjectService, ID: policy.AdmissionKey}
	}
	if snapshot.request.RunID == "" {
		input, err := trustedIntakeRequest(request, principal, team, policy)
		if err != nil {
			return AdmissionResult{}, err
		}
		if input.OriginMachine, err = originMachineTx(ctx, tx, team, principal, request.OriginMachine, request.IdempotencyKey); err != nil {
			return AdmissionResult{}, err
		}
		if replayKey := appReplayKey(input); input.Mode == runspec.IntakeExecute && replayKey != "" {
			var previous string
			err := tx.QueryRowContext(ctx, `SELECT id FROM triggers WHERE team=? AND webhook_replay_key<>'' AND webhook_replay_key=?`, team, replayKey).Scan(&previous)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return AdmissionResult{}, err
			}
			if err == nil {
				if _, err := intakeAuthorityTx(ctx, tx, team, request); err != nil {
					return AdmissionResult{}, err
				}
				return admissionResultTx(ctx, tx, team, previous)
			}
		}
		depth, inherited, err := validateChildLineageTx(ctx, tx, team, &input, principal.Capability)
		if err != nil {
			return AdmissionResult{}, err
		}
		if depth > 0 {
			if err = enforceChildLimitsTx(ctx, tx, team, input.Lineage.RootRunID, depth); err != nil {
				return AdmissionResult{}, err
			}
			charge = inherited
			if charge.AdmissionSubject.Kind == "" {
				return AdmissionResult{}, authorityError(apierr.IntegrityFailure, "parent run has no charged admission subject")
			}
		}
		if input.Origin == runspec.OriginSchedule && tx.scheduleAdmission == nil {
			return AdmissionResult{}, authorityError(apierr.Forbidden, "only a schedule firing starts a schedule run")
		}
		if err := checkActionsOwnerTx(ctx, tx, team, input); err != nil {
			return AdmissionResult{}, err
		}
		owner, err := trustedAppRunnerTx(ctx, tx, request)
		if err != nil {
			return AdmissionResult{}, err
		}
		owner.apply(&input.Options)
		provenance, err := sealProvenanceTx(ctx, tx, team, request, principal, input, id)
		if err != nil {
			return AdmissionResult{}, err
		}
		pools, err := intakePoolsTx(ctx, tx, team, &input, provenance)
		if err != nil {
			return AdmissionResult{}, err
		}
		if request.Request.Dispatch == "" {
			input.Dispatch = SelectDispatch(input)
		}
		snapshot.request, err = runspec.SealRequest(input, runspec.IntakeFacts{RunID: id, AdmittedAt: now, AllowedPools: pools, AuthorityRef: request.Authority, Ceilings: policy.Ceilings, Provenance: provenance})
		if err != nil {
			return AdmissionResult{}, err
		}
		snapshot.execution.Lineage = input.Lineage
	}
	snapshot.execution.admissionIdentity = charge
	if _, err = intakeAuthorityTx(ctx, tx, team, request); err != nil {
		return AdmissionResult{}, err
	}
	var number int64
	if err = tx.QueryRowContext(ctx, `SELECT next_run_number FROM teams WHERE name=?`, team).Scan(&number); err != nil {
		return AdmissionResult{}, err
	}
	result := AdmissionResult{RunID: id, Number: number, Request: snapshot.request, Inputs: runspec.DisplayRequest(snapshot.request)}
	if snapshot.spec != nil {
		result.Inputs = runspec.DisplaySpec(*snapshot.spec)
	}
	if request.IdempotencyKey == "" {
		scope = ""
		digest = ""
	}
	if err = enforceRunsPerHourTx(ctx, tx, team, id, charge.AdmissionSubject, now); err != nil {
		var refused *ComputeLimitError
		if errors.As(err, &refused) {
			err = errors.Join(err, &apierr.Error{Code: apierr.RateLimited, Message: refused.Error(), Details: map[string]any{"reason": "compute_limit", "limit": refused.Limit, "cap": refused.Cap, "observed": refused.Observed, "scope": refused.Scope, "retry_after_ms": 5000}})
		}
		return AdmissionResult{}, err
	}
	if err = insertAdmissionTx(ctx, tx, team, result, snapshot, principal, request.IdempotencyKey, scope, string(digest), policy, now); err != nil {
		return AdmissionResult{}, err
	}
	if err := bindSourceUploadTx(ctx, tx, team, request, result, principal); err != nil {
		return AdmissionResult{}, err
	}
	input := snapshot.request.Request
	if input.Mode == runspec.IntakeExecute {
		result.OwnerUnit = &runstate.OwnerLeaseRef{Kind: runstate.OwnerTrigger, EvalFence: runspec.EvalFence{RunID: runspec.RunID(id)}}
		if input.Options.Runner.Set && input.Options.Runner.Value.Kind == runspec.RunnerHere {
			issued, err := tx.store.Issue(ctx, tx, capability.IssueRequest{Kind: capability.Award, Award: &capability.AwardSpec{Claimant: request.Authority, Unit: capability.UnitTrigger, UnitID: id, Purpose: capability.PurposeHere, Delivery: capability.DeliveryDirect, HereRunID: id}})
			if err != nil {
				return AdmissionResult{}, err
			}
			result.HereCapabilityPrefix, result.HereBearer = issued.Capability.Prefix, []byte(issued.Raw)
		}
	}
	if snapshot.spec != nil {
		if err = writeAcceptedPlanTx(ctx, tx, team, id, *snapshot.spec, snapshot.plan, 0); err != nil {
			return AdmissionResult{}, err
		}
	}
	if _, err = intakeAuthorityTx(ctx, tx, team, request); err != nil {
		return AdmissionResult{}, err
	}
	counter, err := tx.ExecContext(ctx, `UPDATE teams SET next_run_number=next_run_number+1 WHERE name=? AND next_run_number=?`, team, number)
	if err != nil {
		return AdmissionResult{}, err
	}
	changed, err := counter.RowsAffected()
	if err != nil {
		return AdmissionResult{}, err
	}
	if changed != 1 {
		return AdmissionResult{}, authorityError(apierr.Conflict, "run number allocation changed")
	}
	if err = writeRunEffectsTx(ctx, tx, team, id, runstate.RunPending, now); err != nil {
		return AdmissionResult{}, err
	}
	if request.VerifiedDeliveryID != "" || snapshot.execution.Retry != nil && snapshot.spec == nil && input.Origin == runspec.OriginApp {
		run, err := readRunTx(ctx, tx, team, id)
		if err != nil {
			return AdmissionResult{}, err
		}
		creditRefusal, err := intakeCreditRefusalTx(ctx, tx, team, run)
		if err != nil {
			return AdmissionResult{}, err
		}
		if creditRefusal != "" {
			if snapshot.execution.Retry != nil {
				return AdmissionResult{}, authorityError(apierr.Conflict, creditRefusal)
			}
			if _, err := transitionRunTx(ctx, tx, team, runstate.RunTransition{RunID: runspec.RunID(id), To: runstate.RunFailed, Actor: runstate.Actor{Kind: runstate.ActorOperator, Authority: request.Authority}, Cause: runCreditRefused, Reason: FailureCreditsExhausted, MaskedError: "run did not start: " + creditRefusal}); err != nil {
				return AdmissionResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE triggers SET status='failed',finished_at=? WHERE team=? AND id=?`, now.UnixNano(), team, id); err != nil {
				return AdmissionResult{}, err
			}
			result.OwnerUnit = nil
		}
	}
	return result, nil
}

// Credit admission judges sealed placement policy, including when every free runner is offline or busy.
func intakeCreditRefusalTx(ctx context.Context, tx *storeTx, team Team, run RunRow) (string, error) {
	request := run.Request.Request
	if request.Mode != runspec.IntakeExecute || request.Dispatch != runspec.DispatchController || request.Burst != nil {
		return "", nil
	}
	options := request.Options
	if run.Spec != nil {
		options = run.Spec.Resolved.Settings.Options
	}
	if runner := options.Runner; runner.Set && runner.Value.Kind != runspec.RunnerAny && runner.Value.Kind != runspec.RunnerLabel {
		return "", nil
	}
	terms := slices.Clone(options.Requires.Value)
	if runner := options.Runner; runner.Set && runner.Value.Kind == runspec.RunnerLabel {
		terms = append(terms, runner.Value.Value)
	}
	// Observed labels and platform never make an otherwise free placement paid-only.
	var selector []string
	for _, term := range terms {
		placementOnly := true
		for _, alternative := range strings.Split(term, ",") {
			key, _, _ := strings.Cut(strings.TrimSpace(alternative), "=")
			if key != "class" && key != "pool" && key != "name" && key != "location" && key != "local" {
				placementOnly = false
			}
		}
		if placementOnly {
			selector = append(selector, term)
		}
	}
	if !match.Evaluate(match.Profile{Class: match.ClassCloud, Location: TierCloud}, match.Demand{Selector: selector}).OK() {
		return "", nil
	}
	paidOnly := !match.Evaluate(match.Profile{Class: match.ClassAgent, Location: "local"}, match.Demand{Selector: selector}).OK() &&
		!match.Evaluate(match.Profile{Class: match.ClassCoordinator, Location: "local"}, match.Demand{Selector: selector}).OK()
	if rankedUnit(request, match.Demand{Selector: options.Requires.Value}) {
		ranking, err := claimRankingTx(ctx, tx, team)
		if err != nil {
			return "", err
		}
		if len(ranking) > 0 {
			if !slices.Contains(ranking, TierCloud) {
				return "", nil
			}
			paidOnly = paidOnly || len(ranking) == 1
		}
	}
	if !paidOnly {
		return "", nil
	}
	unit, err := ownerUnitTx(ctx, tx, team, run.ID)
	if err != nil {
		return "", err
	}
	return unitCreditHoldTx(ctx, tx, team, run, unit)
}

func intakeDigest(request IntakeRequest, snapshot admittedSnapshot) (runspec.Hash, error) {
	intent := request.Request
	if request.Intent != nil {
		intent = *request.Intent
	}
	var identity any = struct {
		Request  runspec.Request
		Delivery string
	}{intent, request.VerifiedDeliveryID}
	if snapshot.execution.Retry != nil {
		identity = struct{ SourceRunID, Mode string }{snapshot.execution.Lineage.RetryOf, snapshot.execution.Retry.Mode}
	}
	return runspec.CanonicalHash(identity)
}

func intakeAuthorityTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest) (capability.Principal, error) {
	// The here proof adds a second authority leaf; discover both before locking either.
	if prefix := capability.HereProofPrefix(request.OriginMachine); prefix != "" {
		machine := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}
		if _, err := discoverAuthorityChainTx(ctx, tx, machine); err == nil {
			if err := tx.operator.lockAuthorityTx(ctx, tx, []capability.AuthorityRef{request.Authority, machine}); err != nil {
				return capability.Principal{}, err
			}
		} else {
			var denied *apierr.Error
			if !errors.As(err, &denied) {
				return capability.Principal{}, err
			}
		}
	}
	p, err := tx.ResolveAuthority(ctx, request.Authority)
	if err != nil {
		return p, err
	}
	c := p.Capability
	if c.Team != string(team) || c.Audience != capability.Controller || request.Request.Team != "" && request.Request.Team != string(team) || request.CapabilityPrefix != "" && request.CapabilityPrefix != c.Prefix {
		return p, authorityError(apierr.Forbidden, "intake authority does not match the team")
	}
	dispatch := request.Request.Dispatch
	if dispatch == "" {
		dispatch = SelectDispatch(request.Request)
	}
	if request.Request.LocalForeground && (request.Request.Mode != runspec.IntakeExecute || request.Request.Origin != runspec.OriginLocal || dispatch != runspec.DispatchHost || c.Subject.Kind != capability.SubjectLocal) {
		return p, authorityError(apierr.Forbidden, "foreground reservation requires local host intake")
	}
	grant := capability.RunsWrite
	if request.Request.Lineage.RetryOf != "" {
		grant = capability.RunsControl
	}
	if request.Request.Lineage.ParentRunID != "" {
		grant = capability.ChildrenStart
	}
	if !slices.Contains(c.Grants, grant) {
		return p, authorityError(apierr.Forbidden, "intake authority lacks the required grant")
	}
	if grant == capability.ChildrenStart {
		if c.Binding.Kind != capability.BindingNode || c.Binding.RunID != request.Request.Lineage.ParentRunID || c.Binding.NodeID != request.Request.Lineage.ParentJobID || request.Request.Lineage.RetryOf != "" {
			return p, holderRefusal()
		}
		if err = lockStateRunTx(ctx, tx, team, c.Binding.RunID); err != nil {
			return p, err
		}
		row, err := readNodeTx(ctx, tx, team, runspec.NodeRef{RunID: runspec.RunID(c.Binding.RunID), NodeID: runspec.NodeID(c.Binding.NodeID)})
		if err != nil {
			return p, err
		}
		actor := runstate.Actor{Kind: runstate.ActorHolder, Authority: request.Authority, CapabilityPrefix: c.Prefix, NodeFence: runspec.NodeFence{RunID: runspec.RunID(c.Binding.RunID), NodeID: runspec.NodeID(c.Binding.NodeID), Holder: c.Binding.Holder, Generation: runspec.ClaimGeneration(c.Binding.Generation)}}
		if row.Status == runstate.StatusDone || row.CancelRequestedAt != nil {
			return p, holderRefusal()
		}

		if err = checkHolderTx(ctx, tx, team, row, actor); err != nil {
			return p, err
		}
		var cancelled sql.NullInt64
		if err = tx.QueryRowContext(ctx, `SELECT cancel_requested_at FROM runs WHERE team=? AND id=?`, team, c.Binding.RunID).Scan(&cancelled); err != nil {
			return p, err
		}
		if cancelled.Valid {
			return p, holderRefusal()
		}
	} else if c.Binding.Kind != capability.BindingNone && c.Binding.Kind != capability.BindingPush {
		return p, holderRefusal()
	}
	if c.Binding.Kind == capability.BindingPush {
		push := c.Binding.Push
		git := request.Request.Git
		if push == nil || string(git.Repository.Key) != push.RepositoryKey || git.Ref != push.Ref || git.SHA != push.SHA || request.Request.Lineage.RetryOf != "" {
			return p, holderRefusal()
		}
	}
	return p, nil
}

func retryIntakeTx(ctx context.Context, tx *storeTx, team Team, request IntakeRequest, p capability.Principal) (admittedSnapshot, error) {
	if request.Request.Lineage.RetryOf == "" {
		if request.RetryMode != "" {
			return admittedSnapshot{}, authorityError(apierr.BadRequest, "retry mode requires a source run")
		}
		return admittedSnapshot{}, nil
	}
	mode := request.RetryMode
	if mode == "" {
		mode = "failed"
	}
	if mode != "failed" && mode != "all" {
		return admittedSnapshot{}, authorityError(apierr.BadRequest, "unknown retry mode")
	}
	if err := lockStateRunTx(ctx, tx, team, request.Request.Lineage.RetryOf); err != nil {
		return admittedSnapshot{}, err
	}
	source, err := readRunTx(ctx, tx, team, request.Request.Lineage.RetryOf)
	if err != nil {
		return admittedSnapshot{}, err
	}
	if request.Request.Pipeline != "" && request.Request.Pipeline != source.Pipeline {
		return admittedSnapshot{}, authorityError(apierr.BadRequest, "retry pipeline differs from its source")
	}
	if source.Request.Request.Mode != runspec.IntakeExecute {
		return admittedSnapshot{}, authorityError(apierr.BadRequest, "published history cannot be retried")
	}
	in := runspec.RetryInput{Request: source.Request, Plan: source.Plan, Retrier: runspec.Actor{Kind: string(p.Capability.Subject.Kind), ID: p.Capability.Subject.ID}, SourceRunID: source.ID}
	if source.Spec != nil {
		in.Spec = runspec.Optional[runspec.Spec]{Set: true, Value: *source.Spec}
	}
	carried, err := runspec.CarryRetry(in)
	if err != nil {
		return admittedSnapshot{}, err
	}
	out := admittedSnapshot{request: carried.Request, plan: carried.Plan, execution: executionLineage{Lineage: carried.Lineage, Retry: &runstate.RetrySelection{Mode: mode}}}
	if carried.Spec.Set {
		out.spec = &carried.Spec.Value
	}
	return out, nil
}

func trustedIntakeRequest(request IntakeRequest, p capability.Principal, team Team, policy IntakePolicyFacts) (runspec.Request, error) {
	input := request.Request
	if input.Burst != nil {
		if p.Capability.Subject.Kind != capability.SubjectAccount {
			return input, bindingRefusal()
		}
		if err := input.Burst.Validate(); err != nil {
			return input, authorityError(apierr.BadRequest, err.Error())
		}
	}
	if input.Pipeline == "" || input.Mode != runspec.IntakeExecute && input.Mode != runspec.IntakePublish || input.Dispatch != "" && input.Dispatch != runspec.DispatchHost && input.Dispatch != runspec.DispatchController {
		return input, authorityError(apierr.BadRequest, "invalid intake mode, dispatch or pipeline")
	}
	input.Team, input.OriginMachine = string(team), ""
	if policy.Options == nil {
		return input, authorityError(apierr.Unavailable, "intake settings resolution is unavailable")
	}
	input.Options = *policy.Options
	input.Actor = runspec.Actor{Kind: string(p.Capability.Subject.Kind), ID: p.Capability.Subject.ID}
	input.Git.App = runspec.Optional[runspec.AppFacts]{}
	input.Git.DefaultBranch = runspec.Optional[string]{}
	if request.VerifiedDeliveryID != "" {
		delivery := policy.Delivery
		if delivery == nil || !delivery.Git.App.Set || delivery.Git.App.Value.DeliveryKey == "" || delivery.Git.App.Value.VerifiedAt.IsZero() {
			return input, authorityError(apierr.Unauthenticated, "verified App delivery is unavailable")
		}
		input.Git, input.Actor, input.PullRequest = delivery.Git, delivery.Actor, delivery.PullRequest
		input.Origin = runspec.OriginApp
	} else if input.Origin == runspec.OriginApp || policy.Delivery != nil {
		return input, authorityError(apierr.Forbidden, "App intake requires a verified delivery")
	}
	if input.PullRequest.Set && (input.PullRequest.Value.Fork || input.PullRequest.Value.HeadRepo != input.PullRequest.Value.BaseRepo) {
		return input, authorityError(apierr.Forbidden, "fork pull requests cannot execute")
	}
	if input.Git.Repository.Key == "" {
		return input, authorityError(apierr.BadRequest, "repository identity is required")
	}
	if policy.SourceLocation != nil {
		input.Source.Location = *policy.SourceLocation
	} else if input.Source.Location != (runspec.SourceLocation{}) {
		return input, authorityError(apierr.Unavailable, "verified source location is unavailable")
	}
	if input.Lineage.ParentRunID != "" {
		input.Origin = runspec.OriginChild
	}
	if input.Mode == runspec.IntakePublish {
		input.Origin = runspec.OriginPublish
	}
	return input, nil
}

// intakePoolsTx grants reserved pools from sealed provenance alone: only a run whose
// identity a signed App delivery or an App-armed schedule established qualifies.
func intakePoolsTx(ctx context.Context, tx *storeTx, team Team, input *runspec.Request, provenance runspec.Provenance) ([]string, error) {
	pools := []string{}
	if !provenance.Verified() && !provenance.TrustedDefault() {
		return pools, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT pool,branch,source,route FROM pool_access_rules WHERE team=? AND repo_key=? ORDER BY pool,id`+tx.forShare(), team, fmt.Sprintf("gh:%d", provenance.RepositoryID()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pool, branch, source, route string
		if err = rows.Scan(&pool, &branch, &source, &route); err != nil {
			return nil, err
		}
		if source != "" && (source != string(runspec.OriginApp) || !provenance.Verified()) {
			continue
		}
		if branch != "" && (!strings.HasPrefix(provenance.Ref(), "refs/heads/") || !(runspec.BranchSelector{branch}).Matches(provenance.Ref())) {
			continue
		}
		if route != "" {
			requirement := "pool=" + pool
			if !slices.Contains(input.Options.Requires.Value, requirement) {
				input.Options.Requires.Value = append(slices.Clone(input.Options.Requires.Value), requirement)
			}
			if !input.Options.Requires.Set {
				input.Options.Requires.Set = true
				input.Options.Requires.Origin = runspec.SettingOrigin{Layer: runspec.LayerOperatorTeam, Key: "pool_access_rules"}
			}
		}
		if !slices.Contains(pools, pool) {
			pools = append(pools, pool)
		}
	}
	return pools, rows.Err()
}

func replayAdmissionTx(ctx context.Context, tx *storeTx, team Team, scope, key, digest string) (AdmissionResult, bool, error) {
	var id, previous string
	err := tx.QueryRowContext(ctx, `SELECT id,idempotency_digest FROM runs WHERE team=? AND idempotency_scope=? AND idempotency_key=?`, team, scope, key).Scan(&id, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return AdmissionResult{}, false, nil
	}
	if err != nil {
		return AdmissionResult{}, false, err
	}
	if previous != digest {
		return AdmissionResult{}, true, authorityError(apierr.Conflict, "idempotency key has different intake inputs")
	}
	result, err := admissionResultTx(ctx, tx, team, id)
	return result, true, err
}

func admissionResultTx(ctx context.Context, tx *storeTx, team Team, id string) (AdmissionResult, error) {
	run, err := readRunTx(ctx, tx, team, id)
	if err != nil {
		return AdmissionResult{}, err
	}
	result := AdmissionResult{RunID: id, Number: run.Number, Request: run.Request, Inputs: run.Display(), OwnerUnit: run.Owner, Replayed: true}
	if run.Request.Request.Mode == runspec.IntakePublish {
		result.OwnerUnit = nil
	}
	if run.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere {
		err = tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE team=? AND bind_run=? AND award_purpose='here' ORDER BY created_at LIMIT 1`, team, id).Scan(&result.HereCapabilityPrefix)
		if err != nil {
			return AdmissionResult{}, err
		}
	}
	return result, nil
}

func appReplayKey(input runspec.Request) string {
	if !input.Git.App.Set {
		return ""
	}
	return gitHubAppReplayKey(input.Git.App.Value.DeliveryKey, input.Pipeline)
}

func gitHubAppReplayKey(digest, pipeline string) string {
	if digest == "" || pipeline == "" {
		return ""
	}
	return digest + ":" + pipeline
}

// validateChildLineageTx checks a child against every ancestor and returns its depth,
// counting the root as one level, or zero for a run that is not a child.
func validateChildLineageTx(ctx context.Context, tx *storeTx, team Team, input *runspec.Request, claim capability.Capability) (int, admissionIdentity, error) {
	var admission admissionIdentity
	input.Lineage.RootRunID = ""
	if input.Lineage.ParentRunID == "" {
		if input.Lineage.RepoInherited {
			return 0, admissionIdentity{}, authorityError(apierr.BadRequest, "repository inheritance requires a parent")
		}
		return 0, admissionIdentity{}, nil
	}
	if !claim.AllowsChild(input.Pipeline, string(input.Git.Repository.Key)) {
		return 0, admissionIdentity{}, authorityError(apierr.Forbidden, "child source is outside the parent claim's declared children")
	}
	seen := map[string]bool{}
	depth := 1
	for id := input.Lineage.ParentRunID; id != ""; {
		if seen[id] {
			return 0, admissionIdentity{}, authorityError(apierr.Conflict, "child pipeline cycle")
		}
		seen[id] = true
		depth++
		ancestor, err := readRunTx(ctx, tx, team, id)
		if err != nil {
			return 0, admissionIdentity{}, err
		}
		parent := ancestor.Request.Request
		if id == input.Lineage.ParentRunID {
			if input.Env.Digest != "" && (input.Env.Digest != parent.Env.Digest || !slices.Equal(input.Env.Names, parent.Env.Names) || input.EnvironmentSource != parent.EnvironmentSource) {
				return 0, admissionIdentity{}, authorityError(apierr.SpecDrift, "child environment differs from its parent")
			}
			input.Env, input.EnvironmentSource = parent.Env, parent.EnvironmentSource
			admission = ancestor.admission
			if parent.Git.Repository.Key == input.Git.Repository.Key {
				input.Git.Repository.Display = parent.Git.Repository.Display
			}
		}
		if parent.Git.Repository.Key == input.Git.Repository.Key && parent.Pipeline == input.Pipeline {
			return 0, admissionIdentity{}, authorityError(apierr.Conflict, "child pipeline cycle")
		}
		input.Lineage.RootRunID = id
		id = ancestor.Lineage.ParentRunID
	}
	return depth, admission, nil
}

func intakeScope(principal capability.Principal, pipeline string) (string, error) {
	subject := principal.Capability.Subject
	if principal.Authority.Kind == "schedule" {
		subject.ID, pipeline = principal.Authority.ID, ""
	}
	return stateHash(struct {
		Subject  capability.Subject
		Pipeline string
	}{subject, pipeline})
}

// safety: here trusts only a live personal machine credential the submitting account owns; other proofs seal nothing.
func originMachineTx(ctx context.Context, tx *storeTx, team Team, p capability.Principal, proof, idempotencyKey string) (string, error) {
	prefix := capability.HereProofPrefix(proof)
	if prefix == "" || p.Capability.Subject.Kind != capability.SubjectAccount {
		return "", nil
	}
	var hereKey, owner string
	err := tx.QueryRowContext(ctx, `SELECT here_proof_public_key,created_by FROM capabilities WHERE team=? AND prefix=?`, team, prefix).Scan(&hereKey, &owner)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !capability.VerifyHereProof(proof, hereKey, idempotencyKey, tx.now(ctx)) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	machine, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
	var denied *apierr.Error
	if errors.As(err, &denied) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	c := machine.Capability
	facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: machine}}, runspec.Request{})
	if err != nil || !facts.Personal || c.Team != string(team) || c.Binding.Kind != capability.BindingNone || owner == "" || owner != p.Capability.Subject.ID {
		return "", err
	}
	return c.Subject.ID, nil
}
