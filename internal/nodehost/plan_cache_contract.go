package nodehost

import (
	"context"
	"encoding/json"

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
	panic("unimplemented: c3-plan-cache")
}

// ValidatePlanCacheHit checks identity and digest, then calls nodeproto.ValidatePlan
// with the current secret matcher and placement. It refuses expansions and replays
// neither stored settings nor old acceptance authority. Acceptance is still mandatory.
func ValidatePlanCacheHit(entry PlanCacheEntry, inputs PlanCacheInputs, validation nodeproto.ValidationContext) (ValidatedPlanHit, error) {
	panic("unimplemented: c3-plan-cache")
}

// PlanningInput supplies the same cache boundary to local, runner, here, Actions
// and controller-owned planning. A nil cache is an explicit host policy, not the default.
type PlanningInput struct {
	Cache      PlanCache
	Inputs     PlanCacheInputs
	Validation nodeproto.ValidationContext
}
