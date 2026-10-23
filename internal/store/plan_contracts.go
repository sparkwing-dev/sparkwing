package store

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func validatePlan(raw json.RawMessage, ctx nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
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

// PlanSecretOpener decrypts a stored envelope without releasing its plaintext to a host.
// The store selects the tenant and pipeline scope inside the acceptance transaction.
type PlanSecretOpener interface {
	OpenPlanSecret(context.Context, Team, Secret) (string, error)
}

func planSecretsTx(ctx context.Context, tx *storeTx, team Team, spec runspec.Spec, selection secretSelection) (*secrets.Masker, error) {
	values := secrets.NewMasker()
	if spec.Request.Request.Origin != runspec.OriginLocal && len(spec.Request.Request.Env.Names) > 0 {
		submitted, err := (&Tenant{s: tx.store, team: team}).readSubmittedEnvironmentTx(ctx, tx, spec.Request.RunID)
		if err != nil {
			return nil, err
		}
		if err := procenv.CheckSubmitted(submitted, values); err != nil {
			return nil, err
		}
	}
	for _, name := range spec.Request.Request.LocalSecretNames {
		value, err := tx.store.localSecret(team, spec.Request.RunID, name)
		if err != nil {
			return nil, err
		}
		if err := values.Register(value); err != nil {
			return nil, authorityError(apierr.PlanInvalid, err.Error())
		}
	}
	registration, err := runspec.BuildSecretRegistration(spec.Request.Request, spec.Resolved)
	if err != nil {
		return nil, authorityError(apierr.PlanInvalid, err.Error())
	}
	for _, value := range registration.Values {
		if err := values.Register(value); err != nil {
			return nil, authorityError(apierr.PlanInvalid, err.Error())
		}
	}
	expansions, err := readAcceptedExpansionsTx(ctx, tx, team, string(spec.Request.RunID))
	if err != nil {
		return nil, err
	}
	for _, expansion := range expansions {
		for _, value := range expansion.Registration.Values {
			if err := values.Register(value); err != nil {
				return nil, authorityError(apierr.PlanInvalid, err.Error())
			}
		}
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
		if err := values.Register(value); err != nil {
			return nil, authorityError(apierr.PlanInvalid, err.Error())
		}
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
