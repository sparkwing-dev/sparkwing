package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// PlanningFunctions connects SDK planning to the shared validation and hash owners.
// Nil Validate and Hash use nodeproto.ValidatePlan and runspec.CanonicalHash.
// Warn receives safe cache diagnostics; a nil Warn uses the process logger.
type PlanningFunctions struct {
	Plan     func(context.Context) (json.RawMessage, error)
	Validate func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error)
	Hash     func(any) (runspec.Hash, error)
	Warn     func(string)
}

// PlannedInitial contains validated declarations, never a seal or execution authority.
// Every result must pass AcceptPlan, including a CacheHit. CacheHit means the SDK
// planner was not called and no planning compute should be charged.
type PlannedInitial struct {
	Plan          nodeproto.PlanDoc
	CanonicalPlan json.RawMessage
	PlanHash      runspec.Hash
	CacheHit      bool
	MissReason    string
}

// PlanInitial reuses a validated cache entry or runs and validates SDK planning.
// Cache storage failures warn and fall back to planning; cancellation stops both.
// Expansions belong to their own transaction and cannot enter this lifecycle.
func PlanInitial(ctx context.Context, input PlanningInput, functions PlanningFunctions) (PlannedInitial, error) {
	if err := context.Cause(ctx); err != nil {
		return PlannedInitial{}, err
	}
	if err := initialPlanningContext(input.Validation); err != nil {
		return PlannedInitial{}, err
	}
	functions = functions.defaults()
	key, eligible, err := PlanCacheIdentity(input.Inputs)
	if err != nil {
		functions.Warn("plan cache identity unavailable; planning normally")
		eligible.Reason = "cache identity unavailable"
	}
	result := PlannedInitial{MissReason: eligible.Reason}
	switch {
	case input.Cache == nil:
		result.MissReason = "plan cache disabled by host policy"
	case eligible.Eligible:
		result = lookupInitialPlan(ctx, input, key, functions)
	}
	if err := context.Cause(ctx); err != nil {
		return PlannedInitial{}, err
	}
	if result.CacheHit {
		return result, nil
	}
	return planFresh(ctx, input, key, eligible.Eligible, result.MissReason, functions)
}

func lookupInitialPlan(ctx context.Context, input PlanningInput, key PlanCacheKey, functions PlanningFunctions) PlannedInitial {
	lookup, err := input.Cache.Lookup(ctx, key)
	if context.Cause(ctx) != nil {
		return PlannedInitial{}
	}
	if err != nil {
		functions.Warn("plan cache lookup failed; planning normally")
		return PlannedInitial{MissReason: "cache lookup failed"}
	}
	if lookup.Entry == nil {
		if lookup.MissReason != "" && lookup.MissReason != "entry is absent" {
			functions.Warn("plan cache entry unavailable; planning normally")
		}
		return PlannedInitial{MissReason: "cache entry unavailable"}
	}
	hit, err := validatePlanCacheHit(*lookup.Entry, input.Inputs, input.Validation, functions)
	if context.Cause(ctx) != nil {
		return PlannedInitial{}
	}
	if err == nil {
		return PlannedInitial{Plan: hit.Plan, CanonicalPlan: hit.CanonicalPlan, PlanHash: hit.PlanHash, CacheHit: true}
	}
	// safety: cache errors may contain secret declarations or private input hashes.
	functions.Warn("plan cache entry failed current validation; planning normally")
	if err := input.Cache.Invalidate(ctx, key, "entry failed current validation"); err != nil && context.Cause(ctx) == nil {
		functions.Warn("plan cache invalidation failed; ignoring the entry")
	}
	return PlannedInitial{MissReason: "cache entry failed current validation"}
}

func planFresh(ctx context.Context, input PlanningInput, key PlanCacheKey, eligible bool, missReason string, functions PlanningFunctions) (PlannedInitial, error) {
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
	if input.Cache != nil && eligible {
		entry := PlanCacheEntry{Key: key, Inputs: input.Inputs, Plan: canonical, PlanHash: hash}
		if err := input.Cache.Put(ctx, entry); err != nil && context.Cause(ctx) == nil {
			functions.Warn("plan cache write failed; using the validated plan")
		}
	}
	if err := context.Cause(ctx); err != nil {
		return PlannedInitial{}, err
	}
	return PlannedInitial{Plan: plan, CanonicalPlan: canonical, PlanHash: hash, MissReason: missReason}, nil
}

func initialPlanningContext(validation nodeproto.ValidationContext) error {
	if validation.Expansion || len(validation.ExistingJobs) != 0 || validation.SourceJob != "" {
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
	if !planCacheDigestValid(hash) {
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
	if f.Warn == nil {
		f.Warn = func(message string) { slog.Warn(message) }
	}
	return f
}
