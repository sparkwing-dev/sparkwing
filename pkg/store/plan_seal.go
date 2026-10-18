package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

func planAuthorityTx(ctx context.Context, tx *storeTx, team Team, request PlanAcceptance) error {
	p, err := tx.ResolveAuthority(ctx, request.Actor.Authority)
	if err != nil {
		return err
	}
	c := p.Capability
	if c.Team != string(team) || c.Audience != capability.Controller || c.Prefix != request.Actor.CapabilityPrefix || c.Binding.RunID != request.RunID {
		return holderRefusal()
	}
	var mode, dispatch string
	var accepted int64
	if err = tx.QueryRowContext(ctx, `SELECT intake_mode,dispatch,plan_accepted_generation FROM runs WHERE team=? AND id=?`, team, request.RunID).Scan(&mode, &dispatch, &accepted); err != nil {
		return err
	}
	if mode == string(runspec.IntakePublish) {
		return checkPublisherTx(ctx, tx, team, request.Actor, request.RunID)
	}
	expected := capability.UnitTrigger
	binding := capability.BindingTrigger
	if dispatch == string(runspec.DispatchController) {
		expected = capability.UnitPlan
		binding = capability.BindingRun
	}
	owner := request.Actor.Owner
	if request.Actor.Kind != ActorRunHost || owner.Kind != OwnerTrigger || owner.RunID != request.RunID || owner.Generation <= 0 || owner.Holder == "" || c.Unit != expected || c.UnitID != request.RunID || c.Binding.Kind != binding || !slices.Contains(c.Grants, capability.NodeResult) || c.Binding.Generation != owner.Generation || c.Binding.Holder != owner.Holder {
		return holderRefusal()
	}
	var holder, prefix, status string
	var generation int64
	var lease, cancelled sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(claimed_by,''),capability_prefix,generation,lease_expires_at,cancel_requested_at FROM triggers WHERE team=? AND id=? AND unit_kind=?`+tx.forUpdate(), team, request.RunID, expected).Scan(&status, &holder, &prefix, &generation, &lease, &cancelled); err != nil {
		return err
	}
	if generation != owner.Generation {
		return holderRefusal()
	}
	if accepted == generation {
		if proof := request.Actor.Result; proof != nil && proof.Pattern == "/api/v1/runs/{run}/plan" {
			var digest string
			if err := tx.QueryRowContext(ctx, `SELECT result_digest FROM capabilities WHERE team=? AND prefix=?`, team, c.Prefix).Scan(&digest); err != nil {
				return err
			}
			if digest != "" {
				if digest != proof.Digest {
					return authorityError(apierr.IllegalTransition, "accepted plan payload differs")
				}
				return validateResultProofTx(ctx, tx, team, request.Actor, NodeRef{RunID: request.RunID}, 0)
			}
		}
	}
	if cancelled.Valid {
		return holderRefusal()
	}
	if status == "done" && expected == capability.UnitPlan && accepted == generation && request.Actor.Result == nil {
		return nil
	}
	if status != "claimed" || holder != owner.Holder || prefix != c.Prefix || !lease.Valid || lease.Int64 <= tx.now().UnixNano() {
		return holderRefusal()
	}
	return nil
}

func preparePlanSealTx(ctx context.Context, tx *storeTx, team Team, request PlanAcceptance, run RunRow) (runspec.Spec, json.RawMessage, error) {
	input := request.Seal
	requested, err := stateHash(input.Request)
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	stored, err := stateHash(run.Request)
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	if requested != stored {
		return runspec.Spec{}, nil, authorityError(apierr.SpecDrift, "request seal cannot change")
	}
	input.Request = run.Request
	input.Now = tx.now()
	if err = validateResolvedIntake(&input); err != nil {
		return runspec.Spec{}, nil, err
	}
	secrets, err := planSecretsTx(ctx, tx, team, runspec.Spec{Request: run.Request, Resolved: input.Resolved})
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	plan, err := tx.store.planServices().ValidatePlan(input.Plan, nodeproto.ValidationContext{Placement: RequestPlacement(run.Request.Request), Secrets: secrets})
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	if plan.Jobs == nil {
		return runspec.Spec{}, nil, authorityError(apierr.PlanInvalid, "plan requires an explicit jobs array")
	}
	resolved, err := tx.store.intakeServices().ResolvePlan(settings.PlanInputs{Request: run.Request, Declarations: input.Declarations, Plan: plan, Context: settings.Context{Local: RequestPlacement(run.Request.Request) == nodeproto.PlacementLocal}})
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	if !reflect.DeepEqual(resolved, input.Resolved.Settings) {
		return runspec.Spec{}, nil, authorityError(apierr.SpecDrift, "submitted settings differ from resolved declarations")
	}
	if len(resolved.Jobs) != len(plan.Jobs) {
		return runspec.Spec{}, nil, authorityError(apierr.PlanInvalid, "resolved settings do not match plan jobs")
	}
	if !localSecretDeclared(input.Resolved.SecretNames, run.Request.Request.LocalSecretNames) {
		return runspec.Spec{}, nil, authorityError(apierr.PlanInvalid, "local secrets must be declared by the pipeline")
	}
	if err = secretPreflightTx(ctx, tx, team, run, input.Resolved.SecretNames); err != nil {
		return runspec.Spec{}, nil, err
	}
	if err = checkExpansionSecretPoolsTx(ctx, tx, team, runspec.Spec{Request: run.Request, Resolved: input.Resolved}, resolved.Jobs); err != nil {
		return runspec.Spec{}, nil, err
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return runspec.Spec{}, nil, err
	}
	input.Plan = body
	spec, err := tx.store.intakeServices().SealSpec(input)
	return spec, body, err
}

func validateResolvedIntake(input *runspec.SealInput) error {
	request := input.Request.Request
	if input.Resolved.SecretArgs == nil {
		return authorityError(apierr.PlanInvalid, "secret argument classification is required")
	}
	input.Resolved.SecretArgs = slices.Clone(input.Resolved.SecretArgs)
	for _, name := range request.SecretArgs.Value {
		if !slices.Contains(input.Resolved.SecretArgs, name) {
			input.Resolved.SecretArgs = append(input.Resolved.SecretArgs, name)
		}
	}
	slices.Sort(input.Resolved.SecretArgs)
	for name, value := range request.RequestedArgs {
		if resolved, exists := input.Resolved.Args[name]; !exists || resolved != value {
			return authorityError(apierr.SpecDrift, "resolved argument changed an intake value")
		}
	}
	if !reflect.DeepEqual(request.Env, input.Resolved.Env) {
		return authorityError(apierr.SpecDrift, "resolved environment changed the intake snapshot")
	}
	if request.Git.App.Set {
		if !input.Resolved.DefaultBranch.Set || input.Resolved.DefaultBranch.Value != request.Git.App.Value.DefaultBranch || input.Resolved.DefaultBranchSource != runspec.DefaultBranchApp {
			return authorityError(apierr.SpecDrift, "resolved default branch changed verified App facts")
		}
	} else if input.Resolved.DefaultBranch.Set && input.Resolved.DefaultBranchSource != runspec.DefaultBranchOriginHEAD {
		return authorityError(apierr.PlanInvalid, "default branch must come from origin HEAD")
	}
	return nil
}
