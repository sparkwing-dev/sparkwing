package sparkwing

import (
	"context"
	"fmt"
	"time"
)

// stepRangeKey scopes the WithStepRange context value.
type stepRangeKey struct{}

// stepRange carries the operator-supplied --start-at / --stop-at
// strings into RunWork. Empty values mean "no bound on that side."
// Both empty = no range filtering applies.
type stepRange struct {
	start string
	stop  string
}

// WithStepRange installs a --start-at / --stop-at range on ctx so
// every Work executed under it can filter its items down to the
// selected window. The orchestrator validates the strings before
// installing -- RunWork applies the filter only to Works that
// actually contain the named bound, so multi-Job pipelines that
// pass the range globally degrade gracefully on Works that don't.
//
// IMP-007: lets `wing <pipeline> --start-at STEP` skip every step
// upstream of STEP and resume from there without authors having to
// hand-roll a stepOrder slice + skipBefore predicate per pipeline.
func WithStepRange(ctx context.Context, startAt, stopAt string) context.Context {
	if startAt == "" && stopAt == "" {
		return ctx
	}
	return context.WithValue(ctx, stepRangeKey{}, stepRange{start: startAt, stop: stopAt})
}

// StepRangeFromContext returns the (startAt, stopAt) bounds plumbed
// onto ctx by WithStepRange. Both empty when no range was set.
// Exported so renderers (e.g. `sparkwing pipeline explain`) can
// preview "what would be skipped" without re-running RunWork.
func StepRangeFromContext(ctx context.Context) (startAt, stopAt string) {
	r, _ := stepRangeFromContext(ctx)
	return r.start, r.stop
}

func stepRangeFromContext(ctx context.Context) (stepRange, bool) {
	r, ok := ctx.Value(stepRangeKey{}).(stepRange)
	if !ok {
		return stepRange{}, false
	}
	return r, true
}

// computeStepRangeSkips returns the set of work-item IDs that should
// be skipped given the requested range, plus a human-readable
// reason for the `step_skipped` event. The semantics intentionally
// mirror the ticket's prose:
//
//   - --start-at X: skip every item NOT in {X} ∪ descendants(X).
//   - --stop-at  Y: skip every item NOT in {Y} ∪ ancestors(Y).
//   - both:        intersect the two keep-sets, skip the rest.
//
// Items the bound doesn't reference (because the bound names a step
// in another Work) leave the keep-set unconstrained on that side.
// The DAG can have multiple valid topological orders; this set-based
// reachability formulation makes the skip decision deterministic
// and parallelism-aware -- "--start-at X on a downstream branch
// skips ALL upstream including sibling branches" falls out naturally.
func computeStepRangeSkips(items map[string]*workItem, children map[string][]string, r stepRange) map[string]string {
	parents := make(map[string][]string, len(items))
	for id, it := range items {
		for _, d := range it.deps {
			parents[id] = append(parents[id], d)
		}
		// Ensure every node has an entry so reachable() is uniform.
		if _, ok := parents[id]; !ok {
			parents[id] = nil
		}
	}

	keep := make(map[string]bool, len(items))
	for id := range items {
		keep[id] = true // optimistic: only narrow when a bound applies
	}

	if _, ok := items[r.start]; ok {
		desc := reachable(r.start, children)
		desc[r.start] = true
		for id := range keep {
			if !desc[id] {
				keep[id] = false
			}
		}
	}
	if _, ok := items[r.stop]; ok {
		anc := reachable(r.stop, parents)
		anc[r.stop] = true
		for id := range keep {
			if !anc[id] {
				keep[id] = false
			}
		}
	}

	skips := make(map[string]string)
	for id, ok := range keep {
		if ok {
			continue
		}
		// Hidden generator items (SpawnNodeForEach synthetics) take
		// their skip from whatever step deps they share -- emitting
		// "skipped" for them would surface internal scheduling ids
		// in the dashboard. Mark them range-skipped so they don't
		// dispatch, but leave isHidden semantics for the renderer.
		skips[id] = stepRangeReasonString(r)
	}
	return skips
}

// reachable returns the set of nodes reachable from `start` by
// following the given adjacency map. start itself is NOT included
// in the returned set (callers add it explicitly when needed).
func reachable(start string, adj map[string][]string) map[string]bool {
	out := make(map[string]bool)
	stack := append([]string(nil), adj[start]...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if out[n] {
			continue
		}
		out[n] = true
		stack = append(stack, adj[n]...)
	}
	return out
}

// stepRangeReasonString formats the skip reason carried in the
// step_skipped event Attrs.
func stepRangeReasonString(r stepRange) string {
	switch {
	case r.start != "" && r.stop != "":
		return fmt.Sprintf("outside --start-at=%s --stop-at=%s range", r.start, r.stop)
	case r.start != "":
		return fmt.Sprintf("upstream of --start-at=%s", r.start)
	case r.stop != "":
		return fmt.Sprintf("downstream of --stop-at=%s", r.stop)
	}
	return ""
}

// emitStepSkippedWithReason emits the `step_skipped` event with the
// range-skip reason on Attrs so renderers can distinguish a user
// SkipIf predicate from a wing-level --start-at/--stop-at filter.
func emitStepSkippedWithReason(ctx context.Context, stepID, reason string) {
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "info",
		Node:  NodeFromContext(ctx),
		Event: "step_skipped",
		Msg:   stepID,
		Attrs: map[string]any{
			"step":    stepID,
			"outcome": "skipped",
			"reason":  reason,
		},
	}))
}

// TopologicalStepOrder returns Work item IDs in a stable topological
// order consistent with their Needs DAG: ties broken by registration
// order (the order Step / SpawnNode / SpawnNodeForEach was called).
// Hidden synthetic items (SpawnNodeForEach generators) appear at
// their natural position; renderers that want a human-readable view
// should filter them. Returns nil for a nil/empty Work.
//
// Exposed so `sparkwing pipeline explain` and friends can render
// "this is what --start-at=X would skip" without dispatching.
func (w *Work) TopologicalStepOrder() []string {
	if w == nil {
		return nil
	}
	steps := w.Steps()
	spawns := w.Spawns()
	gens := w.SpawnGens()
	total := len(steps) + len(spawns) + len(gens)
	if total == 0 {
		return nil
	}

	// Insertion order, mirroring RunWork's items map population.
	regOrder := make([]string, 0, total)
	deps := make(map[string][]string, total)
	known := make(map[string]bool, total)
	add := func(id string, d []string) {
		if known[id] {
			return
		}
		known[id] = true
		regOrder = append(regOrder, id)
		deps[id] = append([]string(nil), d...)
	}
	for _, s := range steps {
		add(s.ID(), s.DepIDs())
	}
	for _, sp := range spawns {
		add(sp.ID(), sp.DepIDs())
	}
	for _, g := range gens {
		add(g.ID(), g.DepIDs())
	}

	indeg := make(map[string]int, total)
	children := make(map[string][]string, total)
	for _, id := range regOrder {
		if _, ok := indeg[id]; !ok {
			indeg[id] = 0
		}
		for _, d := range deps[id] {
			if !known[d] {
				continue
			}
			indeg[id]++
			children[d] = append(children[d], id)
		}
	}

	// Kahn's with FIFO over registration order: ties resolve to the
	// step Step()'d earliest, so renderers see a stable layout.
	pos := make(map[string]int, total)
	for i, id := range regOrder {
		pos[id] = i
	}

	out := make([]string, 0, total)
	pending := append([]string(nil), regOrder...)
	for len(out) < total {
		// Pick the registration-earliest item with indeg 0.
		picked := ""
		pickedIdx := -1
		for i, id := range pending {
			if id == "" {
				continue
			}
			if indeg[id] == 0 {
				if picked == "" || pos[id] < pos[picked] {
					picked = id
					pickedIdx = i
				}
			}
		}
		if picked == "" {
			// Cycle -- bail with what we have. RunWork surfaces the
			// real error at dispatch time.
			break
		}
		pending[pickedIdx] = ""
		out = append(out, picked)
		for _, c := range children[picked] {
			indeg[c]--
		}
	}
	return out
}
