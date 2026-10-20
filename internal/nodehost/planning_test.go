package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type planningFixture struct {
	input          PlanningInput
	raw            json.RawMessage
	plans          int
	validations    int
	rejectNext     bool
	seenValidation nodeproto.ValidationContext
	warnings       []string
}

func newPlanningFixture(t *testing.T) *planningFixture {
	t.Helper()
	return &planningFixture{
		raw: json.RawMessage(`{ "jobs": [{"id": "test"}] }`),
		input: PlanningInput{Validation: nodeproto.ValidationContext{
			Placement: nodeproto.PlacementLocal, Secrets: planningSecretMatcher{},
		}},
	}
}

func (f *planningFixture) functions() PlanningFunctions {
	return PlanningFunctions{
		Plan: func(context.Context) (json.RawMessage, error) { f.plans++; return f.raw, nil },
		Validate: func(raw json.RawMessage, validation nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
			f.validations++
			f.seenValidation = validation
			if f.rejectNext {
				f.rejectNext = false
				return nodeproto.PlanDoc{}, errors.New("current validation refused the declaration")
			}
			var plan nodeproto.PlanDoc
			err := json.Unmarshal(raw, &plan)
			return plan, err
		},
		Hash: planningTestHash,
	}
}

func planningTestHash(value any) (runspec.Hash, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return runspec.Hash(hex.EncodeToString(digest[:])), nil
}

type planningSecretMatcher struct{}

func (planningSecretMatcher) ContainsSecret(value string) bool { return false }

func TestPlanInitialAlwaysRunsCurrentSDKPlanning(t *testing.T) {
	calls := 0
	for range 2 {
		result, err := PlanInitial(t.Context(), PlanningInput{Validation: nodeproto.ValidationContext{Secrets: planningSecretMatcher{}, Placement: nodeproto.PlacementLocal}}, PlanningFunctions{Plan: func(context.Context) (json.RawMessage, error) { calls++; return json.RawMessage(`{"jobs":[]}`), nil }})
		if err != nil || len(result.Plan.Jobs) != 0 || result.PlanHash == "" {
			t.Fatalf("planning = %+v, %v", result, err)
		}
	}
	if calls != 2 {
		t.Fatalf("SDK calls = %d, want 2", calls)
	}
}

func TestPlanInitialRetainsAuthorityLossAtEveryBoundary(t *testing.T) {
	for _, stage := range []string{"before", "plan", "validate", "hash"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newPlanningFixture(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			functions := fixture.functions()
			switch stage {
			case "before":
				cancel(lease.ErrLeaseLost)
			case "plan":
				plan := functions.Plan
				functions.Plan = func(ctx context.Context) (json.RawMessage, error) {
					cancel(lease.ErrLeaseLost)
					return plan(ctx)
				}
			case "validate":
				validate := functions.Validate
				functions.Validate = func(raw json.RawMessage, validation nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					cancel(lease.ErrLeaseLost)
					return validate(raw, validation)
				}
			case "hash":
				hash := functions.Hash
				functions.Hash = func(value any) (runspec.Hash, error) {
					cancel(lease.ErrLeaseLost)
					return hash(value)
				}
			}
			result, err := PlanInitial(ctx, fixture.input, functions)
			if !errors.Is(err, lease.ErrLeaseLost) || len(result.CanonicalPlan) != 0 {
				t.Fatalf("%s lost authority: result=%+v error=%v", stage, result, err)
			}
		})
	}
}
