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
	profile.Labels = AuthenticatedLabels(profile)
	if len(profile.ReservedPools) > 0 && !requiredReservation(profile.ReservedPools, demand) {
		return Verdict{Reason: ReasonReservation}
	}
	return evaluateRequirements(profile, demand, false)
}

// PlacementPossible checks granted placement facts while treating ordinary labels
// and platform requirements as satisfiable, without relying on online runners.
// It uses the same AND/OR selector evaluation as eligibility.
func PlacementPossible(profile Profile, selector []string) bool {
	profile.Labels = AuthenticatedLabels(profile)
	return evaluateRequirements(profile, Demand{Selector: selector}, true).OK()
}

// AuthenticatedLabels projects self-reported labels and granted pools, retaining
// catalog size labels only for a Cloud profile.
func AuthenticatedLabels(profile Profile) []string {
	labels := profile.Labels
	profile.Labels = SelfAsserted(profile.Labels)
	if profile.Class == ClassCloud {
		for _, size := range CloudSizes() {
			if slices.Contains(labels, size.Label) {
				profile.Labels = append(profile.Labels, size.Label)
			}
		}
	}
	for _, pool := range profile.ReservedPools {
		profile.Labels = append(profile.Labels, "pool="+pool)
	}
	return profile.Labels
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
