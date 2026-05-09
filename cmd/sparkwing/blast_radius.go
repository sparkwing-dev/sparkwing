// blast-radius dispatch gate. Reads the author-declared per-step
// markers from the per-repo describe cache and refuses dispatch when
// a Destructive / AffectsProduction / CostsMoney step is reachable
// without the matching --allow-* escape (or --dry-run). Stale or
// missing cache silently degrades to "no markers detected, no gate
// fires" so the gate is purely additive and never blocks a dispatch
// the cache hasn't seen -- mirrors the venue gate.
package main

import (
	"github.com/sparkwing-dev/sparkwing/profile"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// blastRadiusFinding is one marker the gate detected on a specific
// step. The first finding the dispatcher hits is the one it surfaces
// in the BlastRadiusBlockedError so the operator sees a concrete
// step name rather than a hand-wavey "this pipeline is destructive."
type blastRadiusFinding struct {
	NodeID string
	StepID string
	Marker sparkwing.BlastRadius
}

// lookupCachedBlastRadius returns the per-step marker list for the
// named pipeline as stored in the describe cache. Returns nil when
// the pipeline isn't in the cache or when reading fails -- callers
// treat empty as "no constraint declared" and proceed without
// gating, matching the venue gate's degrade-gracefully shape.
func lookupCachedBlastRadius(sparkwingDir, pipelineName string) []blastRadiusFinding {
	schemas, err := readDescribeCache(sparkwingDir)
	if err != nil || schemas == nil {
		return nil
	}
	for _, s := range schemas {
		if s.Name != pipelineName {
			continue
		}
		var out []blastRadiusFinding
		for _, row := range s.BlastRadiusBySteps {
			for _, m := range row.Markers {
				br := sparkwing.BlastRadius(m)
				if !br.IsValid() {
					// Unknown wire token -- ignore so a future marker
					// in a newer SDK doesn't accidentally block here.
					continue
				}
				out = append(out, blastRadiusFinding{
					NodeID: row.NodeID,
					StepID: row.StepID,
					Marker: br,
				})
			}
		}
		// Fallback to the union list when the per-step breakdown is
		// empty but the union is populated. This happens for
		// pipelines whose Plan() can't run under empty args during
		// --describe but whose union was filled in by a previous
		// SDK that surfaced both fields. Best-effort -- node/step
		// ids are unknown so the error message references the
		// pipeline only.
		if len(out) == 0 {
			for _, m := range s.BlastRadius {
				br := sparkwing.BlastRadius(m)
				if !br.IsValid() {
					continue
				}
				out = append(out, blastRadiusFinding{Marker: br})
			}
		}
		return out
	}
	return nil
}

// enforceBlastRadius is the dispatcher's gate: refuse the run when
// any reachable step declares a marker the operator hasn't
// authorized via the matching --allow-* flag. --dry-run bypasses
// every gate (the safe-mode contract), and a profile-level
// auto-allow (laptop / kind cluster) can pre-authorize specific
// markers so a known-safe environment doesn't pester the user.
//
// Returns nil when no marker fires or when every fired marker is
// authorized. Returns a *sparkwing.BlastRadiusBlockedError on the
// first refusal so callers can pattern-match.
func enforceBlastRadius(
	pipelineName string,
	findings []blastRadiusFinding,
	wf wingFlags,
	prof *profile.Profile,
) error {
	// dry-run is the always-safe escape hatch.
	// Authors declare a DryRunFn (or SafeWithoutDryRun) and the
	// orchestrator runs the no-mutation body in place of the apply
	// Fn. Bypassing the gate here matches that contract.
	if wf.dryRun {
		return nil
	}
	for _, f := range findings {
		if isMarkerAllowed(f.Marker, wf, prof) {
			continue
		}
		return &sparkwing.BlastRadiusBlockedError{
			Pipeline: pipelineName,
			StepID:   f.StepID,
			Marker:   f.Marker,
		}
	}
	return nil
}

// isMarkerAllowed reports whether the operator has authorized the
// given marker via either the wing-level --allow-* flag or the
// profile's auto_allow declaration. Profile auto-allow is opt-in
// per-marker so a "production" profile can leave Destructive locked
// while a "laptop" profile can flip the whole set.
func isMarkerAllowed(b sparkwing.BlastRadius, wf wingFlags, prof *profile.Profile) bool {
	switch b {
	case sparkwing.BlastRadiusDestructive:
		if wf.allowDestructive {
			return true
		}
		if prof != nil && prof.AutoAllow.Destructive {
			return true
		}
	case sparkwing.BlastRadiusAffectsProduction:
		if wf.allowProd {
			return true
		}
		if prof != nil && prof.AutoAllow.Production {
			return true
		}
	case sparkwing.BlastRadiusCostsMoney:
		if wf.allowMoney {
			return true
		}
		if prof != nil && prof.AutoAllow.Money {
			return true
		}
	}
	return false
}
