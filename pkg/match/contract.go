package match

import (
	"slices"
	"strings"
)

// Evaluate is the shared eligibility and preference decision for every named,
// queued and self claim. Profile identity and reservations are granted facts;
// labels cannot impersonate name, class or pool. Demand contains only resolved
// requirements, preferences and intake-authorized pools.
// A reserved profile is eligible only when at least one granted pool is both
// explicitly required by the demand and present in AllowedPools. Matching a
// different alternative in a selector never substitutes for that pool requirement.
// Repository, reservation and hard requirements precede preference scoring.
// Preferences rank eligible candidates and never change the admission verdict.
func Evaluate(profile Profile, demand Demand) Verdict {
	profile.Labels = SelfAsserted(profile.Labels)
	for _, pool := range profile.ReservedPools {
		profile.Labels = append(profile.Labels, "pool="+pool)
	}
	if len(profile.ReservedPools) > 0 && !requiredReservation(profile.ReservedPools, demand) {
		return Verdict{Reason: ReasonReservation}
	}
	if verdict := evaluateRequirements(profile, demand); !verdict.OK() {
		return verdict
	}
	var verdict Verdict
	for _, preference := range demand.Prefers {
		if strings.TrimSpace(preference) != "" && TermSatisfied(profile, preference) {
			verdict.PreferenceScore++
		}
	}
	return verdict
}

func requiredReservation(pools []string, demand Demand) bool {
	for _, term := range demand.Selector {
		for _, alternative := range strings.Split(term, ",") {
			pool, found := strings.CutPrefix(strings.TrimSpace(alternative), "pool=")
			if found && pool != "" && slices.Contains(pools, pool) && slices.Contains(demand.AllowedPools, pool) {
				return true
			}
		}
	}
	return false
}
