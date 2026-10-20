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

// PlanningFunctions connects SDK planning to the shared validation and hash owners.
// Nil Validate and Hash use nodeproto.ValidatePlan and runspec.CanonicalHash.
type PlanningFunctions struct {
	Plan     func(context.Context) (json.RawMessage, error)
	Validate func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error)
	Hash     func(any) (runspec.Hash, error)
}

// PlanningInput supplies current validation for initial SDK planning.
type PlanningInput struct{ Validation nodeproto.ValidationContext }

// PlannedInitial contains validated declarations for AcceptPlan.
type PlannedInitial struct {
	Plan          nodeproto.PlanDoc
	CanonicalPlan json.RawMessage
	PlanHash      runspec.Hash
}

// PlanInitial runs and validates SDK planning before acceptance.
func PlanInitial(ctx context.Context, input PlanningInput, functions PlanningFunctions) (PlannedInitial, error) {
	if err := context.Cause(ctx); err != nil {
		return PlannedInitial{}, err
	}
	if err := initialPlanningContext(input.Validation); err != nil {
		return PlannedInitial{}, err
	}
	functions = functions.defaults()
	if functions.Plan == nil {
		return PlannedInitial{}, errors.New("initial planning requires an SDK planner")
	}
	raw, err := functions.Plan(ctx)
	if context.Cause(ctx) != nil {
		return PlannedInitial{}, context.Cause(ctx)
	}
	if err != nil {
		return PlannedInitial{}, err
	}
	plan, err := functions.Validate(raw, input.Validation)
	if context.Cause(ctx) != nil {
		return PlannedInitial{}, context.Cause(ctx)
	}
	if err != nil {
		return PlannedInitial{}, err
	}
	canonical, hash, err := canonicalPlanningDocument(raw, functions.Hash)
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

func canonicalPlanningDocument(raw json.RawMessage, hashPlan func(any) (runspec.Hash, error)) (json.RawMessage, runspec.Hash, error) {
	hash, err := hashPlan(raw)
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

func (f PlanningFunctions) defaults() PlanningFunctions {
	if f.Validate == nil {
		f.Validate = nodeproto.ValidatePlan
	}
	if f.Hash == nil {
		f.Hash = runspec.CanonicalHash
	}
	return f
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
