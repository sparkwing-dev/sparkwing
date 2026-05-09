package sparkwing

import "fmt"

// BlastRadius is a typed marker an author attaches to a WorkStep to
// declare the consequences of running it. Without markers every step
// is equally trusted -- `wing cluster-down` and `wing format` look
// identical from the dispatcher's POV. Agents make mistakes humans
// wouldn't ("wait, this would destroy production" is an instinct),
// and even careful humans benefit from explicit markers. With
// blast-radius markers the *author* declares the contract and the
// *operator/agent* runs into explicit gates before a destructive
// dispatch.
//
// Markers are additive: a single step may declare any combination
// (e.g. `apply-eks` is both Destructive and AffectsProduction). The
// dispatcher walks the per-step set against the wing-level escape
// flags (--allow-destructive / --allow-prod / --allow-money) plus
// --dry-run; `wing X --dry-run` always proceeds regardless of the
// markers, because dry-run is the safe-mode preview path.
//
// Mirrors the Venue contract (sparkwing/venue.go): typed values
// flow through DescribePipeline + PlanPreview + planSnapshot as
// kebab-case strings so renderers, agents, and the CLI dispatcher
// all read the same wire format.
type BlastRadius string

const (
	// BlastRadiusDestructive marks a step that mutates external state
	// in a way that's hard or impossible to undo: deleting a cluster,
	// dropping a database, force-pushing a release tag. The dispatcher
	// gates this behind --allow-destructive (or --dry-run).
	BlastRadiusDestructive BlastRadius = "destructive"

	// BlastRadiusAffectsProduction marks a step that touches
	// production state -- prod database, prod cluster, prod registry,
	// prod customers. Independent of Destructive: a prod read may be
	// safe to repeat but still merits an explicit gate because a
	// laptop-side typo can still leak data or burn credentials.
	BlastRadiusAffectsProduction BlastRadius = "production"

	// BlastRadiusCostsMoney marks a step that incurs spend the
	// operator should consciously authorize: spinning up a fleet of
	// expensive instances, calling a paid API, running a long
	// distributed job. The cost is the gate, not the destruction.
	BlastRadiusCostsMoney BlastRadius = "money"
)

// String returns the canonical kebab-case wire token used in JSON
// snapshots and error messages.
func (b BlastRadius) String() string { return string(b) }

// IsValid reports whether b is one of the canonical declared
// markers. Used by the wire-decoder so a stale or older
// describe cache file with garbage values silently degrades to
// "no marker" rather than misclassifying a step.
func (b BlastRadius) IsValid() bool {
	switch b {
	case BlastRadiusDestructive,
		BlastRadiusAffectsProduction,
		BlastRadiusCostsMoney:
		return true
	}
	return false
}

// AllBlastRadii returns the canonical marker set, sorted by wire
// token. Documented use: scaffolders / linters that want to render
// "valid markers: ..." hints.
func AllBlastRadii() []BlastRadius {
	return []BlastRadius{
		BlastRadiusDestructive,
		BlastRadiusAffectsProduction,
		BlastRadiusCostsMoney,
	}
}

// BlastRadiusBlockedError is the typed error returned when the
// dispatcher refuses a run because a step declares a blast-radius
// marker the operator hasn't acknowledged via the matching
// --allow-* flag (or --dry-run). Carries enough structured data
// that JSON consumers (agents, dashboard) can pattern-match without
// parsing the message.
//
// Mirrors VenueMismatchError's role for the venue gate.
type BlastRadiusBlockedError struct {
	Pipeline string
	StepID   string
	Marker   BlastRadius
}

func (e *BlastRadiusBlockedError) Error() string {
	flag := allowFlagFor(e.Marker)
	return fmt.Sprintf(
		"step %q in pipeline %q is marked %s; pass --%s to confirm or --dry-run to preview.",
		e.StepID, e.Pipeline, e.Marker, flag,
	)
}

// Destructive marks a WorkStep as performing destructive work --
// state mutation that's hard or impossible to undo. The dispatcher
// refuses to run a pipeline containing a destructive step unless
// the operator passes --allow-destructive (or --dry-run, which
// bypasses every blast-radius gate by contract).
//
//	sparkwing.Step(w, "destroy-eks", j.destroyEKS).
//	    Destructive().
//	    AffectsProduction()
func (s *WorkStep) Destructive() *WorkStep {
	s.addBlastRadius(BlastRadiusDestructive)
	return s
}

// AffectsProduction marks a WorkStep as touching production state
// (prod database, prod cluster, prod registry, prod customers).
// The dispatcher refuses to run a pipeline containing such a step
// unless the operator passes --allow-prod (or --dry-run).
//
// Independent of Destructive: a prod read may be reversible but
// still merits an explicit gate so a laptop-side typo can't leak
// data or burn credentials silently.
func (s *WorkStep) AffectsProduction() *WorkStep {
	s.addBlastRadius(BlastRadiusAffectsProduction)
	return s
}

// CostsMoney marks a WorkStep as incurring spend the operator
// should consciously authorize -- spinning up expensive instances,
// calling a paid API, running a long distributed job. The
// dispatcher refuses to run a pipeline containing such a step
// unless the operator passes --allow-money (or --dry-run).
func (s *WorkStep) CostsMoney() *WorkStep {
	s.addBlastRadius(BlastRadiusCostsMoney)
	return s
}

// BlastRadius returns the typed marker set declared on this step,
// in declaration order with duplicates collapsed. Empty when no
// marker was declared -- the canonical "no gate fires" state.
func (s *WorkStep) BlastRadius() []BlastRadius {
	out := make([]BlastRadius, len(s.blastRadius))
	copy(out, s.blastRadius)
	return out
}

// addBlastRadius appends b to the step's marker set, deduplicating
// repeat declarations. The method is internal because authors call
// the typed wrappers (Destructive / AffectsProduction / CostsMoney);
// keeping the storage detail private leaves room to swap the slice
// for a set later without breaking the public surface.
func (s *WorkStep) addBlastRadius(b BlastRadius) {
	for _, existing := range s.blastRadius {
		if existing == b {
			return
		}
	}
	s.blastRadius = append(s.blastRadius, b)
}

// allowFlagFor returns the wing-level escape flag name that
// authorizes a given marker.
func allowFlagFor(b BlastRadius) string {
	switch b {
	case BlastRadiusDestructive:
		return "allow-destructive"
	case BlastRadiusAffectsProduction:
		return "allow-prod"
	case BlastRadiusCostsMoney:
		return "allow-money"
	default:
		return "allow-" + string(b)
	}
}
