package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
)

type planContracts interface {
	ValidatePlan(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error)
	ResolveExpansion(settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error)
	CanonicalHash(any) (runspec.Hash, error)
}

type sharedPlanContracts struct{}

func (sharedPlanContracts) CanonicalHash(value any) (runspec.Hash, error) {
	return runspec.CanonicalHash(value)
}

func (sharedPlanContracts) ValidatePlan(raw json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
	plan, err := nodeproto.ValidatePlan(raw, ctx)
	if err != nil {
		return nodeproto.PlanDoc{}, err
	}
	for _, job := range plan.Jobs {
		for _, child := range job.Children {
			if child.Repository != "" {
				if err := runspec.ValidateRepoUse(runspec.RepoKey(child.Repository), runspec.RepoMachineStore); err != nil {
					return nodeproto.PlanDoc{}, authorityError(apierr.PlanInvalid, "child repository must be a canonical repository key")
				}
			}
		}
	}
	return plan, nil
}

func (sharedPlanContracts) ResolveExpansion(in settings.ExpansionInputs) (map[string]runspec.JobExecutionSettings, error) {
	return settings.ResolveExpansion(in)
}

func (s *Store) planServices() planContracts {
	if s.plans != nil {
		return s.plans
	}
	return sharedPlanContracts{}
}

// PlanSecretOpener decrypts a stored envelope without releasing its plaintext to a host.
// The store selects the tenant and pipeline scope inside the acceptance transaction.
type PlanSecretOpener interface {
	OpenPlanSecret(context.Context, Team, Secret) (string, error)
}

type planSecretValues []string

func (values planSecretValues) ContainsSecret(identifier string) bool {
	return slices.ContainsFunc(values, func(value string) bool { return value != "" && strings.Contains(identifier, value) })
}

func planSecretsTx(ctx context.Context, tx *storeTx, team Team, spec runspec.Spec, selection secretSelection) (planSecretValues, error) {
	values := planSecretValues{}
	if spec.Request.Request.Origin != runspec.OriginLocal && len(spec.Request.Request.Env.Names) > 0 {
		submitted, err := (&Tenant{s: tx.store, team: team}).readSubmittedEnvironmentTx(ctx, tx, spec.Request.RunID)
		if err != nil {
			return nil, err
		}
		masker := secrets.NewMasker()
		if err := procenv.CheckSubmitted(submitted, masker); err != nil {
			return nil, err
		}
		values = append(values, masker.Values()...)
	}
	for _, name := range spec.Request.Request.LocalSecretNames {
		value, err := tx.store.localSecret(team, spec.Request.RunID, name)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	for _, name := range spec.Resolved.SecretArgs {
		value, exists := spec.Resolved.Args[name]
		if !exists {
			return nil, authorityError(apierr.PlanInvalid, "secret argument classification names an absent argument")
		}
		values = append(values, value)
	}
	rows, err := selectedSecretsTx(ctx, tx, team, "", selection)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		secret := row.secret
		if !secret.Masked {
			continue
		}
		if tx.store.planSecretOpener == nil {
			return nil, authorityError(apierr.Unavailable, "plan secret decryption is not configured")
		}
		value, err := tx.store.planSecretOpener.OpenPlanSecret(ctx, team, secret)
		if err != nil {
			return nil, authorityError(apierr.Unavailable, "plan secret decryption failed")
		}
		values = append(values, value)
	}
	return values, nil
}

// RequestPlacement classifies execution consistently at intake and plan acceptance.
func RequestPlacement(request runspec.Request) nodeproto.Placement {
	if request.Mode == runspec.IntakePublish {
		return nodeproto.PlacementLocal
	}
	if request.Dispatch == runspec.DispatchController {
		return nodeproto.PlacementLauncher
	}
	if request.Options.Runner.Set && request.Options.Runner.Value.Kind == runspec.RunnerHere {
		return nodeproto.PlacementHere
	}
	switch request.Origin {
	case runspec.OriginLocal:
		return nodeproto.PlacementLocal
	case runspec.OriginActions:
		return nodeproto.PlacementGitHubActions
	default:
		if request.ExecutionLocation == "local" || request.Source.Location.LocalCheckout != "" {
			return nodeproto.PlacementLocalTriggered
		}
		return nodeproto.PlacementRunnerHosted
	}
}
