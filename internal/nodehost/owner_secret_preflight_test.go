package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestOwnerHostMissingRequiredSecretFinishesUnacceptedRun(t *testing.T) {
	f := newStoreOwnerFixture(t, openStore(t), runspec.Request{
		Version: 1, Team: "default", Pipeline: "missing-secret", Mode: runspec.IntakeExecute,
		Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost,
		Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}},
	})
	row, err := f.Tenant.ReadRun(t.Context(), f.Input.RunID)
	if err != nil {
		t.Fatal(err)
	}
	selected := engineid.Identity{Version: "v0.68.0", ManifestDigest: "manifest", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v0.68.0"}}
	f.Host.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
		return &source.Entry{Engine: engine.Resolved{Identity: selected}}, nil
	}
	f.Host.Prepare = func(context.Context, RunInput, *source.Entry) (OwnerPlanning, error) {
		return OwnerPlanning{
			Declarations: runspec.CheckoutDeclarations{Version: 1},
			Planning:     PlanningInput{Validation: nodeproto.ValidationContext{Secrets: ownerSealSecrets{}, Placement: nodeproto.PlacementLocal}},
			Plan:         func(context.Context) (json.RawMessage, error) { return json.RawMessage(`{"jobs":[]}`), nil },
			Resolve: func(plan nodeproto.PlanDoc) (runspec.Resolved, error) {
				resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: row.Request, Declarations: runspec.CheckoutDeclarations{Version: 1}, Plan: plan})
				return runspec.Resolved{Engine: selected, Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{"BAR"}, Settings: resolved, Guards: runspec.Guards{Passed: true}}, err
			},
		}, nil
	}
	_, err = f.Host.Run(t.Context(), f.Input)
	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Code != apierr.PlanInvalid || !strings.Contains(err.Error(), "sparkwing secrets set --secret BAR --file <path>") {
		t.Fatalf("required-secret refusal: %v", err)
	}
	run, err := f.Tenant.ReadRun(t.Context(), f.Input.RunID)
	if err != nil || run.State != runstate.RunFailed || run.FinishedAt == nil || run.Spec != nil {
		t.Fatalf("refused plan: state=%s finished=%v spec=%v error=%v", run.State, run.FinishedAt, run.Spec, err)
	}
	detail, err := f.Tenant.GetRun(t.Context(), f.Input.RunID)
	if err != nil || !strings.Contains(detail.Error, "sparkwing secrets set --secret BAR --file <path>") {
		t.Fatalf("terminal run lost secret repair advice: %+v, %v", detail, err)
	}
}
