// IMP-011: pipeline-venue dispatch gate. Reads the author-declared
// Venue() metadata from the per-repo describe cache and refuses
// `--on PROFILE` for LocalOnly pipelines / refuses bare invocation
// for ClusterOnly. Stale or missing cache silently degrades to
// VenueEither so the gate is purely additive and never blocks a
// dispatch the cache hasn't seen.
package main

import (
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// lookupCachedVenue returns the venue string ("either" / "local-only"
// / "cluster-only") for the named pipeline as stored in the describe
// cache. Returns "" when the pipeline isn't in the cache or when
// reading fails -- callers treat empty as "no constraint declared"
// and proceed without gating, matching the behavior pre-IMP-011.
func lookupCachedVenue(sparkwingDir, pipelineName string) string {
	schemas, err := readDescribeCache(sparkwingDir)
	if err != nil || schemas == nil {
		return ""
	}
	for _, s := range schemas {
		if s.Name == pipelineName {
			return s.Venue
		}
	}
	return ""
}

// enforcePipelineVenue is the dispatcher's gate: refuse remote
// dispatch for LocalOnly pipelines, refuse bare invocation for
// ClusterOnly. Mirrors sparkwing.EnforceVenue's contract but accepts
// the wire-format venue string from the describe cache so the wing
// CLI doesn't need an in-memory Registration. Empty / unknown venue
// strings are treated as "either" (no gate) so a stale cache or a
// pipeline binary built before IMP-011 can't accidentally block a
// dispatch.
func enforcePipelineVenue(venueStr, pipelineName, on string) error {
	v := parseVenue(venueStr)
	return sparkwing.EnforceVenue(v, pipelineName, on)
}

// parseVenue maps the wire-format venue string back to the typed
// constant. Unknown / empty values map to VenueEither so the gate
// degrades gracefully on stale or pre-IMP-011 cache files.
func parseVenue(s string) sparkwing.Venue {
	switch s {
	case "local-only":
		return sparkwing.VenueLocalOnly
	case "cluster-only":
		return sparkwing.VenueClusterOnly
	default:
		return sparkwing.VenueEither
	}
}
