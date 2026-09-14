package controller

import (
	"fmt"
	"sort"
	"strings"
)

// Limits profiles are the named sets of guard values a hosted controller runs
// with. An install that names none keeps the shipped defaults, where every
// budget below is zero and so unlimited.
const (
	// LimitsProfileCloud is what a paid hosted controller runs with.
	LimitsProfileCloud = "cloud"
	// LimitsProfileCloudFree is what a free-tier hosted controller runs with:
	// the same guards, sized for one runner rather than a pool.
	LimitsProfileCloudFree = "cloud-free"
)

// LimitsProfileValues are the guards one profile supplies. A profile fills a
// guard only where the operator named none, so an explicit flag or environment
// variable always wins.
type LimitsProfileValues struct {
	// ClaimsPerRunnerMinute is [RequestBudget.ClaimsPerMinute].
	ClaimsPerRunnerMinute int
	// HeartbeatsPerRunnerMinute is [RequestBudget.HeartbeatsPerMinute].
	HeartbeatsPerRunnerMinute int
	// MaxLogStreamsPerPrincipal is the concurrent live log streams one
	// principal may hold open.
	MaxLogStreamsPerPrincipal int
	// MaxDownloadsPerPrincipal is the concurrent metered downloads one
	// principal may hold open.
	MaxDownloadsPerPrincipal int
	// EnforceIdleClaimPoll refuses a claim that arrives sooner than the idle
	// interval the controller last suggested that runner. See
	// [Server.WithIdleClaimPollEnforced].
	EnforceIdleClaimPoll bool
}

// safety: the claim budget is spent per runner rather than per offer slot, so
// the hosted profiles are sized for the concurrency an agent is configured
// with: the shipped runner chart sets two, and eight is the headroom a team
// can grow to without an operator raising the budget by hand.
const (
	cloudProfileSlots     = 8
	cloudFreeProfileSlots = 2
)

// safety: the free tier halves the paid request budget and takes a fifth of
// its egress slots, which is the ratio the hosted tiers are priced on.
const freeHeartbeatDivisor = 2

var limitsProfiles = map[string]LimitsProfileValues{
	LimitsProfileCloud: {
		ClaimsPerRunnerMinute:     RecommendedClaimsPerMinuteForSlots(cloudProfileSlots),
		HeartbeatsPerRunnerMinute: RecommendedHeartbeatsPerMinute,
		MaxLogStreamsPerPrincipal: 50,
		MaxDownloadsPerPrincipal:  20,
		EnforceIdleClaimPoll:      true,
	},
	LimitsProfileCloudFree: {
		ClaimsPerRunnerMinute:     RecommendedClaimsPerMinuteForSlots(cloudFreeProfileSlots),
		HeartbeatsPerRunnerMinute: RecommendedHeartbeatsPerMinute / freeHeartbeatDivisor,
		MaxLogStreamsPerPrincipal: 10,
		MaxDownloadsPerPrincipal:  5,
		EnforceIdleClaimPoll:      true,
	},
}

// LimitsProfile returns the guards name supplies. The empty name is the public
// default and supplies none, so an install that asks for no profile keeps
// every budget unlimited. An unknown name is an error rather than a silent
// default, because a typo would otherwise ship a hosted controller unguarded.
func LimitsProfile(name string) (LimitsProfileValues, error) {
	if name == "" {
		return LimitsProfileValues{}, nil
	}
	values, ok := limitsProfiles[name]
	if !ok {
		return LimitsProfileValues{}, fmt.Errorf(
			"unknown limits profile %q; want one of %s", name, strings.Join(LimitsProfileNames(), ", "))
	}
	return values, nil
}

// LimitsProfileNames returns every profile name, sorted.
func LimitsProfileNames() []string {
	out := make([]string, 0, len(limitsProfiles))
	for name := range limitsProfiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
