package match

// Evaluate is the shared eligibility and preference decision for every named,
// queued and self claim. Profile identity and reservations are granted facts;
// labels cannot impersonate name, class or pool. Demand contains only resolved
// requirements, preferences, resources and intake-authorized pools.
// A reserved profile is eligible only when at least one granted pool is both
// explicitly required by the demand and present in AllowedPools. Matching a
// different alternative in a selector never substitutes for that pool requirement.
// Repository, reservation, hard requirements and capacity checks precede scoring.
// Preferences rank eligible candidates and never change the admission verdict.
func Evaluate(profile Profile, demand Demand) Verdict {
	panic("unimplemented: c1-placement")
}
