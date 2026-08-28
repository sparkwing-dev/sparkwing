package sparkwing

import (
	"context"
	"fmt"
	"time"
)

type stepRangeKey struct{}

type stepRange struct {
	start string
	stop  string
}

func stepRangeFromContext(ctx context.Context) (stepRange, bool) {
	v, ok := ctx.Value(stepRangeKey{}).([2]string)
	if !ok || (v[0] == "" && v[1] == "") {
		return stepRange{}, false
	}
	return stepRange{start: v[0], stop: v[1]}, true
}

func computeStepRangeSkips(items map[string]*workItem, children map[string][]string, r stepRange) map[string]string {
	parents := make(map[string][]string, len(items))
	for id, it := range items {
		parents[id] = append(parents[id], it.deps...)
		if _, ok := parents[id]; !ok {
			parents[id] = nil
		}
	}

	keep := make(map[string]bool, len(items))
	for id := range items {
		keep[id] = true
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
		skips[id] = stepRangeReasonString(r)
	}
	return skips
}

// PreviewSkipForRange computes the (id -> human-readable reason)
// skip set this Work would apply under the given --start-at /
// --stop-at bounds, WITHOUT executing any step body. Empty bounds
// (or bounds that don't match any item in this Work) return an
// empty map. Called from `pipeline plan` so the renderer can show
// "would skip: outside --start-at..--stop-at range" alongside the
// static DAG -- terraform-style plan output for sparkwing.
func (w *Work) PreviewSkipForRange(startAt, stopAt string) map[string]string {
	if w == nil || (startAt == "" && stopAt == "") {
		return nil
	}
	steps := w.Steps()
	spawns := w.Spawns()
	gens := w.SpawnGens()
	items := make(map[string]*workItem, len(steps)+len(spawns)+len(gens))
	add := func(id string, deps []string) {
		if _, exists := items[id]; exists {
			return
		}
		items[id] = &workItem{id: id, deps: append([]string(nil), deps...)}
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
	if _, hasStart := items[startAt]; startAt != "" && !hasStart {
		if _, hasStop := items[stopAt]; stopAt == "" || !hasStop {
			return nil
		}
	}
	children := make(map[string][]string, len(items))
	for id, it := range items {
		for _, d := range it.deps {
			if _, ok := items[d]; ok {
				children[d] = append(children[d], id)
			}
		}
	}
	return computeStepRangeSkips(items, children, stepRange{start: startAt, stop: stopAt})
}

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

func emitStepSkippedWithReason(ctx context.Context, stepID, reason string) {
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "info",
		JobID: NodeFromContext(ctx),
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

	pos := make(map[string]int, total)
	for i, id := range regOrder {
		pos[id] = i
	}

	out := make([]string, 0, total)
	pending := append([]string(nil), regOrder...)
	for len(out) < total {
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
