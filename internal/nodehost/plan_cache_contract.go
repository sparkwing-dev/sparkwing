package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// PlanCacheEnabledByDefault applies to planning only; NoMemo controls job-result reuse.
const PlanCacheEnabledByDefault = true

// PlanCacheInputs identifies every input visible to planning. Digests include
// explicit presence and are private because low-entropy secret values are guessable.
// SourceDigest includes the exact tree (including dirty local files), dependencies
// and generated entry inputs. EntryDigest identifies the executable or TS module tree.
// RunContextDigest includes the complete SDK-visible run context, including IDs and
// times. Cross-run reuse cannot omit those fields without an SDK determinism guarantee.
// RunnerDigest includes granted identity, labels, OS and architecture. SecretVersion
// invalidates declaration/matcher changes without persisting secret values here.
type PlanCacheInputs struct {
	Version           int
	Team              string
	Repository        runspec.RepoKey
	Engine            engine.Identity
	Pipeline          string
	Entrypoint        string
	SourceDigest      runspec.Hash
	EntryDigest       runspec.Hash
	ProjectDigest     runspec.Hash
	ArgsDigest        runspec.Hash
	EnvironmentDigest runspec.Hash
	RunContextDigest  runspec.Hash
	RunnerDigest      runspec.Hash
	CheckoutDigest    runspec.Hash
	DescribeDigest    runspec.Hash
	SecretVersion     string
}

// PlanCacheKey is local to the authorized team/repository and format version.
// Key equality grants no access to another cache scope.
type PlanCacheKey struct {
	Version    int
	Team       string
	Repository runspec.RepoKey
	Digest     runspec.Hash
}

// PlanCacheEligibility requires complete stable inputs. An unhashable or externally
// varying planning input yields a miss and normal planning, never a rejected run.
type PlanCacheEligibility struct {
	Eligible bool
	Reason   string
}

// PlanCacheEntry stores declarations, never accepted seals, claim authority or code closures.
// Integrity is checked against PlanHash; malformed or corrupt recoverable entries miss loudly.
type PlanCacheEntry struct {
	Key      PlanCacheKey
	Inputs   PlanCacheInputs
	Plan     json.RawMessage
	PlanHash runspec.Hash
}

// PlanCacheLookup distinguishes a miss from a storage failure without treating either as a hit.
type PlanCacheLookup struct {
	Entry      *PlanCacheEntry
	MissReason string
}

// ValidatedPlanHit proves structural, current-secret and capability validation only.
// It cannot authorize execution: AcceptPlan still resolves settings/ceilings,
// rechecks authority and secret preflight, seals point two and inserts jobs in one
// transaction. Planning compute is zero on this path, so no planning charge accrues.
type ValidatedPlanHit struct {
	Key           PlanCacheKey
	Plan          nodeproto.PlanDoc
	CanonicalPlan json.RawMessage
	PlanHash      runspec.Hash
}

// PlanCache owns the default-enabled cache in the C3 planning lifecycle. Writes
// take freshly validated declarations; cache publication grants no execution rights.
// Cache I/O failure warns and plans normally. Invalid entries are removed, not used.
type PlanCache interface {
	Lookup(context.Context, PlanCacheKey) (PlanCacheLookup, error)
	Put(context.Context, PlanCacheEntry) error
	Invalidate(context.Context, PlanCacheKey, string) error
}

// PlanCacheIdentity canonicalizes complete inputs with stable ordering and presence.
// Different engine/SDK, code, project, args, env, runner, checkout or context means
// a different key. Ineligible inputs bypass cache lookup and execute planning.
func PlanCacheIdentity(inputs PlanCacheInputs) (PlanCacheKey, PlanCacheEligibility, error) {
	if reason := planCacheIneligible(inputs); reason != "" {
		return PlanCacheKey{}, PlanCacheEligibility{Reason: reason}, nil
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return PlanCacheKey{}, PlanCacheEligibility{}, err
	}
	digest := sha256.Sum256(raw)
	return PlanCacheKey{
		Version: inputs.Version, Team: inputs.Team, Repository: inputs.Repository,
		Digest: runspec.Hash(hex.EncodeToString(digest[:])),
	}, PlanCacheEligibility{Eligible: true}, nil
}

func planCacheIneligible(inputs PlanCacheInputs) string {
	if inputs.Version <= 0 {
		return "cache format version is absent"
	}
	for _, field := range []struct{ name, value string }{
		{"team", inputs.Team},
		{"repository", string(inputs.Repository)},
		{"pipeline", inputs.Pipeline},
		{"entrypoint", inputs.Entrypoint},
		{"SDK kind", string(inputs.Engine.Pin.SDK)},
		{"SDK version", inputs.Engine.Pin.Version},
		{"SDK manifest", inputs.Engine.Pin.Manifest},
		{"engine version", inputs.Engine.Version},
		{"secret registry version", inputs.SecretVersion},
	} {
		if field.value == "" || !utf8.ValidString(field.value) {
			return field.name + " is absent or invalid"
		}
	}
	if !utf8.ValidString(inputs.Engine.Pin.Lockfile) {
		return "SDK lockfile is invalid"
	}
	for _, field := range []struct {
		name  string
		value runspec.Hash
	}{
		{"SDK inputs", runspec.Hash(inputs.Engine.Pin.InputsDigest)},
		{"engine manifest", runspec.Hash(inputs.Engine.ManifestDigest)},
		{"source", inputs.SourceDigest},
		{"entry", inputs.EntryDigest},
		{"project", inputs.ProjectDigest},
		{"args", inputs.ArgsDigest},
		{"environment", inputs.EnvironmentDigest},
		{"run context", inputs.RunContextDigest},
		{"runner", inputs.RunnerDigest},
		{"checkout", inputs.CheckoutDigest},
		{"describe", inputs.DescribeDigest},
	} {
		if !planCacheDigestValid(field.value) {
			return field.name + " digest is absent or invalid"
		}
	}
	return ""
}

func planCacheDigestValid(digest runspec.Hash) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	for _, c := range digest {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ValidatePlanCacheHit checks identity and digest, then calls nodeproto.ValidatePlan
// with the current secret matcher and placement. It refuses expansions and replays
// neither stored settings nor old acceptance authority. Acceptance is still mandatory.
func ValidatePlanCacheHit(entry PlanCacheEntry, inputs PlanCacheInputs, validation nodeproto.ValidationContext) (ValidatedPlanHit, error) {
	if validation.Expansion || len(validation.ExistingJobs) != 0 || validation.SourceJob != "" {
		return ValidatedPlanHit{}, errors.New("plan cache cannot supply generator expansions")
	}
	if validation.Secrets == nil {
		return ValidatedPlanHit{}, errors.New("plan cache requires the current secret matcher")
	}
	if err := planCacheEntryIdentity(entry); err != nil {
		return ValidatedPlanHit{}, err
	}
	key, eligible, err := PlanCacheIdentity(inputs)
	if err != nil {
		return ValidatedPlanHit{}, err
	}
	if !eligible.Eligible || key != entry.Key {
		return ValidatedPlanHit{}, errors.New("plan cache entry does not match current planning inputs")
	}
	hash, err := runspec.CanonicalHash(entry.Plan)
	if err != nil {
		return ValidatedPlanHit{}, err
	}
	if hash != entry.PlanHash {
		return ValidatedPlanHit{}, errors.New("plan cache plan hash mismatch")
	}
	plan, err := nodeproto.ValidatePlan(entry.Plan, validation)
	if err != nil {
		return ValidatedPlanHit{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(entry.Plan))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ValidatedPlanHit{}, errors.New("plan cache plan cannot be canonicalized")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return ValidatedPlanHit{}, errors.New("plan cache plan cannot be canonicalized")
	}
	return ValidatedPlanHit{Key: key, Plan: plan, CanonicalPlan: canonical, PlanHash: hash}, nil
}

// PlanningInput supplies the same cache boundary to local, runner, here, Actions
// and controller-owned planning. A nil cache is an explicit host policy, not the default.
type PlanningInput struct {
	Cache      PlanCache
	Inputs     PlanCacheInputs
	Validation nodeproto.ValidationContext
}
