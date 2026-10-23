package match

import (
	"slices"
	"strings"
)

// Evaluate checks a runner profile against resolved requirements and
// intake-authorized pools. Claim awards also enforce eligibility in the store.
// Profile identity and reservations are granted facts; labels cannot
// impersonate name, class or pool.
// A reserved profile is eligible only when at least one granted pool is both
// explicitly required by the demand and present in AllowedPools. Matching a
// different alternative in a selector never substitutes for that pool requirement.
func Evaluate(profile Profile, demand Demand) Verdict {
	profile.Labels = SelfAsserted(profile.Labels)
	for _, pool := range profile.ReservedPools {
		profile.Labels = append(profile.Labels, "pool="+pool)
	}
	if len(profile.ReservedPools) > 0 && !requiredReservation(profile.ReservedPools, demand) {
		return Verdict{Reason: ReasonReservation}
	}
	return evaluateRequirements(profile, demand)
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
