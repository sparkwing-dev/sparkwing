package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestPlanInitialCacheHitSkipsPlanningAndRevalidates(t *testing.T) {
	f := newPlanningFixture(t)
	first, err := PlanInitial(t.Context(), f.input, f.functions())
	if err != nil || first.CacheHit || first.MissReason == "" || f.plans != 1 || f.validations != 1 || len(f.cache.puts) != 1 {
		t.Fatalf("cold plan = %+v, %v; fixture = %+v", first, err, f)
	}
	f.input.Validation.Placement = nodeproto.PlacementRunnerHosted
	warm, err := PlanInitial(t.Context(), f.input, f.functions())
	if err != nil || !warm.CacheHit || warm.MissReason != "" || f.plans != 1 || f.validations != 2 || len(f.cache.puts) != 1 {
		t.Fatalf("warm plan = %+v, %v; fixture = %+v", warm, err, f)
	}
	if !reflect.DeepEqual(warm.Plan, first.Plan) || warm.PlanHash != first.PlanHash || string(warm.CanonicalPlan) != string(first.CanonicalPlan) {
		t.Fatal("cache hit changed the validated declarations")
	}
	if f.seenValidation.Placement != nodeproto.PlacementRunnerHosted || f.seenValidation.Secrets != f.input.Validation.Secrets {
		t.Fatalf("cached plan ignored current validation: %+v", f.seenValidation)
	}
}

func TestPlanInitialFileCacheSurvivesReopen(t *testing.T) {
	f := newPlanningFixture(t)
	directory := t.TempDir()
	cache, err := NewFilePlanCache(directory, f.input.Inputs.Team, f.input.Inputs.Repository)
	if err != nil {
		t.Fatal(err)
	}
	f.input.Cache = cache
	first, err := PlanInitial(t.Context(), f.input, f.functions())
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFilePlanCache(directory, f.input.Inputs.Team, f.input.Inputs.Repository)
	if err != nil {
		t.Fatal(err)
	}
	f.input.Cache = reopened
	warm, err := PlanInitial(t.Context(), f.input, f.functions())
	if err != nil || !warm.CacheHit || f.plans != 1 || warm.PlanHash != first.PlanHash || string(warm.CanonicalPlan) != string(first.CanonicalPlan) {
		t.Fatalf("reopened cache = %+v, %v; plans = %d", warm, err, f.plans)
	}
	key, _, err := PlanCacheIdentity(f.input.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Put(t.Context(), PlanCacheEntry{Key: key, Inputs: f.input.Inputs, Plan: first.CanonicalPlan, PlanHash: runspec.Hash(strings.Repeat("f", 64))}); err != nil {
		t.Fatal(err)
	}
	replacement, err := PlanInitial(t.Context(), f.input, f.functions())
	if err != nil || replacement.CacheHit || f.plans != 2 || replacement.PlanHash != first.PlanHash || len(f.warnings) != 1 {
		t.Fatalf("corrupt hash = %+v, %v; plans = %d, warnings = %v", replacement, err, f.plans, f.warnings)
	}
}

func TestPlanInitialInvalidEntryIsRemovedBeforeFreshPlanning(t *testing.T) {
	for _, kind := range []string{"scope", "inputs", "hash", "malformed", "current validation"} {
		t.Run(kind, func(t *testing.T) {
			f := newPlanningFixture(t)
			if _, err := PlanInitial(t.Context(), f.input, f.functions()); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "scope":
				f.cache.entry.Key.Team = "foreign-team"
			case "inputs":
				f.cache.entry.Inputs.SecretVersion = "stale"
			case "hash":
				f.cache.entry.PlanHash = runspec.Hash(strings.Repeat("f", 64))
			case "malformed":
				f.cache.entry.Plan = json.RawMessage(`{"jobs":`)
			case "current validation":
				f.rejectNext = true
			}
			result, err := PlanInitial(t.Context(), f.input, f.functions())
			if err != nil || result.CacheHit || result.MissReason == "" || f.plans != 2 || len(f.cache.invalidated) != 1 || len(f.warnings) != 1 {
				t.Fatalf("invalid entry = %+v, %v; fixture = %+v", result, err, f)
			}
			if !reflect.DeepEqual(f.cache.calls, []string{"lookup", "put", "lookup", "invalidate", "put"}) {
				t.Fatalf("cache calls = %v", f.cache.calls)
			}
		})
	}
}

func TestPlanInitialCacheFailuresWarnWithoutFailingRun(t *testing.T) {
	for _, operation := range []string{"lookup", "put", "invalidate", "damaged miss"} {
		t.Run(operation, func(t *testing.T) {
			f := newPlanningFixture(t)
			private := "private-secret-bearing-cache-key"
			switch operation {
			case "lookup":
				f.cache.lookupErr = errors.New(private)
			case "put":
				f.cache.putErr = errors.New(private)
			case "invalidate":
				if _, err := PlanInitial(t.Context(), f.input, f.functions()); err != nil {
					t.Fatal(err)
				}
				f.cache.entry.PlanHash = runspec.Hash(strings.Repeat("f", 64))
				f.cache.invalidateErr = errors.New(private)
			case "damaged miss":
				f.cache.missReason = private
			}
			result, err := PlanInitial(t.Context(), f.input, f.functions())
			if err != nil || result.CacheHit || len(f.warnings) == 0 {
				t.Fatalf("cache failure = %+v, %v; warnings = %v", result, err, f.warnings)
			}
			if strings.Contains(strings.Join(f.warnings, " ")+result.MissReason, private) {
				t.Fatal("cache diagnostics exposed private backend content")
			}
		})
	}
}

func TestPlanInitialIncompleteInputsBypassCache(t *testing.T) {
	f := newPlanningFixture(t)
	f.input.Inputs.EnvironmentDigest = ""
	result, err := PlanInitial(t.Context(), f.input, f.functions())
	if err != nil || result.CacheHit || result.MissReason == "" || f.plans != 1 || len(f.cache.calls) != 0 {
		t.Fatalf("ineligible plan = %+v, %v; fixture = %+v", result, err, f)
	}
	f.input.Cache = nil
	result, err = PlanInitial(t.Context(), f.input, f.functions())
	if err != nil || result.CacheHit || result.MissReason == "" || f.plans != 2 || len(f.cache.calls) != 0 {
		t.Fatalf("disabled cache = %+v, %v; fixture = %+v", result, err, f)
	}
}

func TestPlanInitialNeverCachesUnvalidatedPlans(t *testing.T) {
	for _, kind := range []string{"planner error", "validation error", "hash error", "invalid hash", "expansion", "existing jobs", "source job", "missing secrets"} {
		t.Run(kind, func(t *testing.T) {
			f := newPlanningFixture(t)
			functions := f.functions()
			switch kind {
			case "planner error":
				functions.Plan = func(context.Context) (json.RawMessage, error) { return nil, errors.New("planner failed") }
			case "validation error":
				f.rejectNext = true
			case "hash error":
				functions.Hash = func(any) (runspec.Hash, error) { return "", errors.New("hash failed") }
			case "invalid hash":
				functions.Hash = func(any) (runspec.Hash, error) { return "not-a-hash", nil }
			case "expansion":
				f.input.Validation.Expansion = true
			case "existing jobs":
				f.input.Validation.ExistingJobs = []nodeproto.JobSpec{{ID: "existing"}}
			case "source job":
				f.input.Validation.SourceJob = "generator"
			case "missing secrets":
				f.input.Validation.Secrets = nil
			}
			if _, err := PlanInitial(t.Context(), f.input, functions); err == nil || len(f.cache.puts) != 0 {
				t.Fatalf("rejected plan err = %v, writes = %d", err, len(f.cache.puts))
			}
		})
	}
}

func TestPlanInitialCancellationStopsCacheAndPlanning(t *testing.T) {
	for _, phase := range []string{"before lookup", "lookup", "planning", "validation", "hash", "put"} {
		t.Run(phase, func(t *testing.T) {
			f := newPlanningFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			functions := f.functions()
			switch phase {
			case "before lookup":
				cancel()
			case "lookup":
				f.cache.onLookup = cancel
			case "planning":
				functions.Plan = func(context.Context) (json.RawMessage, error) { cancel(); return f.raw, nil }
			case "validation":
				functions.Validate = func(json.RawMessage, nodeproto.ValidationContext) (nodeproto.PlanDoc, error) {
					cancel()
					return nodeproto.PlanDoc{}, nil
				}
			case "hash":
				functions.Hash = func(value any) (runspec.Hash, error) { cancel(); return planningTestHash(value) }
			case "put":
				f.cache.onPut = cancel
			}
			if _, err := PlanInitial(ctx, f.input, functions); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled plan = %v", err)
			}
			if phase != "put" && len(f.cache.puts) != 0 {
				t.Fatal("cancelled planning published a cache entry")
			}
		})
	}
}

type planningFixture struct {
	input          PlanningInput
	cache          *planningCacheFake
	raw            json.RawMessage
	plans          int
	validations    int
	rejectNext     bool
	seenValidation nodeproto.ValidationContext
	warnings       []string
}

func newPlanningFixture(t *testing.T) *planningFixture {
	t.Helper()
	cache := &planningCacheFake{}
	return &planningFixture{
		cache: cache, raw: json.RawMessage(`{ "jobs": [{"id": "test"}] }`),
		input: PlanningInput{Cache: cache, Inputs: planCacheInputs(), Validation: nodeproto.ValidationContext{
			Placement: nodeproto.PlacementLocal, Secrets: planCacheSecretMatcher{},
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
		Warn: func(message string) { f.warnings = append(f.warnings, message) },
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

type planningCacheFake struct {
	entry                            *PlanCacheEntry
	puts                             []PlanCacheEntry
	invalidated                      []PlanCacheKey
	calls                            []string
	missReason                       string
	lookupErr, putErr, invalidateErr error
	onLookup, onPut                  func()
}

func (c *planningCacheFake) Lookup(context.Context, PlanCacheKey) (PlanCacheLookup, error) {
	c.calls = append(c.calls, "lookup")
	if c.onLookup != nil {
		c.onLookup()
	}
	if c.entry == nil && c.missReason == "" {
		return PlanCacheLookup{MissReason: "entry is absent"}, c.lookupErr
	}
	return PlanCacheLookup{Entry: c.entry, MissReason: c.missReason}, c.lookupErr
}

func (c *planningCacheFake) Put(_ context.Context, entry PlanCacheEntry) error {
	c.calls = append(c.calls, "put")
	if c.onPut != nil {
		c.onPut()
	}
	if c.putErr != nil {
		return c.putErr
	}
	c.puts = append(c.puts, entry)
	c.entry = &entry
	return nil
}

func (c *planningCacheFake) Invalidate(_ context.Context, key PlanCacheKey, _ string) error {
	c.calls = append(c.calls, "invalidate")
	c.invalidated = append(c.invalidated, key)
	if c.invalidateErr != nil {
		return c.invalidateErr
	}
	c.entry = nil
	return nil
}
