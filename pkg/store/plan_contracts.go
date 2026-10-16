package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
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
	return nodeproto.ValidatePlan(raw, ctx)
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

// SetPlanSecretOpener configures server-side identifier validation before the store is shared.
// Acceptance refuses encrypted secret validation when no opener is configured.
func (s *Store) SetPlanSecretOpener(opener PlanSecretOpener) {
	s.planSecretOpener = opener
}

type planSecretValues []string

func (values planSecretValues) ContainsSecret(identifier string) bool {
	return slices.ContainsFunc(values, func(value string) bool { return value != "" && strings.Contains(identifier, value) })
}

func planSecretsTx(ctx context.Context, tx *storeTx, team Team, spec runspec.Spec) (planSecretValues, error) {
	values := planSecretValues{}
	for _, name := range spec.Resolved.SecretArgs {
		value, exists := spec.Resolved.Args[name]
		if !exists {
			return nil, authorityError(apierr.PlanInvalid, "secret argument classification names an absent argument")
		}
		values = append(values, value)
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,value,principal,pipeline,masked,shared FROM secrets WHERE team=? AND (pipeline=? OR (pipeline='' AND shared=1)) ORDER BY name,pipeline`+tx.forShare(), team, spec.Request.Request.Pipeline)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var secret Secret
		if err := rows.Scan(&secret.Name, &secret.Value, &secret.Principal, &secret.Pipeline, &secret.Masked, &secret.Shared); err != nil {
			return nil, err
		}
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
	return values, rows.Err()
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
