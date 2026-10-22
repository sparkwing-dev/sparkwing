package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// PlanningInput supplies current validation for initial SDK planning.
type PlanningInput struct {
	Validation   nodeproto.ValidationContext
	declarations *runspec.DeclarationGate
}

// PlannedInitial contains validated declarations for AcceptPlan.
type PlannedInitial struct {
	Plan          nodeproto.PlanDoc
	CanonicalPlan json.RawMessage
	PlanHash      runspec.Hash
}

// PlanInitial runs and validates SDK planning before acceptance.
func PlanInitial(ctx context.Context, input PlanningInput, planSDK func(context.Context) (json.RawMessage, error)) (result PlannedInitial, err error) {
	defer func() {
		if err != nil && input.declarations != nil && context.Cause(ctx) == nil {
			err = input.declarations.Error(err)
		}
	}()
	if err := context.Cause(ctx); err != nil {
		return PlannedInitial{}, err
	}
	if err := initialPlanningContext(input.Validation); err != nil {
		return PlannedInitial{}, err
	}
	if planSDK == nil {
		return PlannedInitial{}, errors.New("initial planning requires an SDK planner")
	}
	raw, err := planSDK(ctx)
	if context.Cause(ctx) != nil {
		return PlannedInitial{}, context.Cause(ctx)
	}
	if err != nil {
		return PlannedInitial{}, err
	}
	plan, err := nodeproto.ValidatePlan(raw, input.Validation)
	if context.Cause(ctx) != nil {
		return PlannedInitial{}, context.Cause(ctx)
	}
	if err != nil {
		return PlannedInitial{}, err
	}
	canonical, hash, err := canonicalPlanningDocument(raw)
	if context.Cause(ctx) != nil {
		return PlannedInitial{}, context.Cause(ctx)
	}
	if err != nil {
		return PlannedInitial{}, err
	}
	return PlannedInitial{Plan: plan, CanonicalPlan: canonical, PlanHash: hash}, nil
}

func initialPlanningContext(validation nodeproto.ValidationContext) error {
	if validation.Expansion || len(validation.ExistingJobs) != 0 || len(validation.ExistingConcurrency) != 0 || validation.SourceJob != "" {
		return errors.New("initial planning cannot supply generator expansions")
	}
	if validation.Secrets == nil {
		return errors.New("initial planning requires the current secret matcher")
	}
	return nil
}

func canonicalPlanningDocument(raw json.RawMessage) (json.RawMessage, runspec.Hash, error) {
	hash, err := runspec.CanonicalHash(raw)
	if err != nil {
		return nil, "", err
	}
	if !planningDigestValid(hash) {
		return nil, "", errors.New("plan hash is invalid")
	}
	if !json.Valid(raw) {
		return nil, "", errors.New("plan cannot be canonicalized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "", errors.New("plan cannot be canonicalized")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize plan: %w", err)
	}
	return canonical, hash, nil
}

func planningDigestValid(digest runspec.Hash) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
