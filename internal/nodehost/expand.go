package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type expansion struct {
	closure string
	nodes   []nodeproto.PlanNode
	err     error
}

type genGroup struct {
	done    bool
	err     error
	members []string
}

// safety: the generators run in the source's own attempt process, after its
// body succeeds, because a generator reads the source's output and the SDK
// holds it in that process; a separate process would read it back through the
// controller, which grants a node only its dependencies' outputs.
func generate(ctx context.Context, sess *Session, n *NodeRun) []expansion {
	if n.Plan.Closures == nil {
		return nil
	}
	out := make([]expansion, 0, len(n.Plan.Closures.Generator))
	for _, id := range n.Plan.Closures.Generator {
		nodes, err := evalGenerator(ctx, sess, id)
		out = append(out, expansion{closure: id, nodes: nodes, err: err})
	}
	return out
}

// safety: the host stamps each spec_hash itself, as it does the plan's, so a
// generator never picks the hash its node is later checked against.
func evalGenerator(ctx context.Context, sess *Session, closure string) ([]nodeproto.PlanNode, error) {
	var res nodeproto.EvalResult
	if err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpEval, Closure: closure}, &res); err != nil {
		return nil, err
	}
	if res.Error != nil {
		return nil, fmt.Errorf("generator %s: %s", closure, *res.Error)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(res.Value, &raws); err != nil {
		return nil, invalid("generator %s returned no array of node documents: %v", closure, err)
	}
	nodes := make([]nodeproto.PlanNode, 0, len(raws))
	for i, raw := range raws {
		hash, err := nodeproto.SpecHash(raw)
		if err != nil {
			return nil, fmt.Errorf("generator %s node %d: %w", closure, i, err)
		}
		var node nodeproto.PlanNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, invalid("generator %s node %d: %v", closure, i, err)
		}
		node.SpecHash = hash
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// safety: a generated node's code is in no process until its generators run
// there, each source's before the next, so its attempt runs the chain and
// compares the node the last returns with what the run accepted.
func regenerate(ctx context.Context, sess *Session, n *NodeRun) error {
	var nodes []nodeproto.PlanNode
	for _, closure := range n.Generators {
		var err error
		if nodes, err = evalGenerator(ctx, sess, closure); err != nil {
			return err
		}
	}
	last := n.Generators[len(n.Generators)-1]
	for _, got := range nodes {
		if got.ID != n.Plan.ID {
			continue
		}
		if got.SpecHash != n.Plan.SpecHash {
			return &nodeproto.Error{Code: nodeproto.CodePlanDrift, Message: fmt.Sprintf(
				"generator %s builds node %s differently than when the run added it; a generator must return the same nodes for the same source output", last, n.Plan.ID)}
		}
		return nil
	}
	return &nodeproto.Error{Code: nodeproto.CodePlanDrift, Message: fmt.Sprintf(
		"generator %s no longer returns node %s; a generator must return the same nodes for the same source output", last, n.Plan.ID)}
}

// safety: every generator of a finished source settles here, whatever its
// outcome, so a node that needs its nodes never waits on a group that cannot finish.
func (s *scheduler) expand(ctx context.Context, source string, res Result) {
	i := slices.IndexFunc(s.nodes, func(n nodeproto.PlanNode) bool { return n.ID == source })
	if i < 0 || s.nodes[i].Closures == nil {
		return
	}
	for _, id := range s.nodes[i].Closures.Generator {
		j := slices.IndexFunc(res.expansions, func(e expansion) bool { return e.closure == id })
		var err error
		switch {
		case !res.Outcome.OK():
			err = fmt.Errorf("expansion source %q did not succeed (outcome=%s)", source, res.Outcome)
		case j < 0:
			err = fmt.Errorf("generator %s did not run", id)
		case res.expansions[j].err != nil:
			err = res.expansions[j].err
		default:
			err = s.addGenerated(ctx, source, res.expansions[j])
		}
		g := s.group(id)
		g.done, g.err = true, err
		if err != nil {
			s.run.Logger.Error("nodehost: expansion failed", "run", s.run.ID, "source", source, "generator", id, "err", err)
		}
	}
}

// safety: a generated node that does not need its source could start before
// the output it was generated from is recorded, so the batch is refused.
func (s *scheduler) addGenerated(ctx context.Context, source string, e expansion) error {
	ids := make(map[string]bool, len(e.nodes))
	for _, n := range e.nodes {
		ids[n.ID] = true
	}
	for _, n := range e.nodes {
		if !slices.Contains(n.Deps, source) && !ids[n.OnFailureOf] {
			return invalid("generator %s returned node %s, which does not need its source %s", e.closure, n.ID, source)
		}
	}
	if err := s.add(ctx, context.WithoutCancel(ctx), e.nodes, &expansionFrom{source: source, closure: e.closure}); err != nil {
		return err
	}
	if s.addedBy == nil {
		s.addedBy, s.sourceOf = map[string]string{}, map[string]string{}
	}
	g := s.group(e.closure)
	children := 0
	for _, n := range e.nodes {
		s.addedBy[n.ID], s.sourceOf[n.ID] = e.closure, source
		if n.OnFailureOf == "" {
			g.members = append(g.members, n.ID)
			children++
		}
	}
	s.run.Logger.Info("nodehost: expansion generated", "run", s.run.ID, "source", source, "generator", e.closure, "nodes", len(e.nodes))
	hostEvent(ctx, s.run, source, "expansion_generated", fmt.Appendf(nil, "%d children", children))
	for _, w := range s.nodes {
		if !slices.Contains(w.NeedsGenerated, e.closure) {
			continue
		}
		deps := append(slices.Clone(w.Deps), w.OptionalDeps...)
		for _, id := range w.NeedsGenerated {
			if g := s.groups[id]; g != nil {
				deps = append(deps, g.members...)
			}
		}
		if err := s.run.State.UpdateNodeDeps(context.WithoutCancel(ctx), s.run.ID, w.ID, deps); err != nil {
			s.run.Logger.Warn("nodehost: record generated deps", "run", s.run.ID, "node", w.ID, "err", err)
		}
	}
	return nil
}

func (s *scheduler) group(closure string) *genGroup {
	if s.groups == nil {
		s.groups = map[string]*genGroup{}
	}
	g := s.groups[closure]
	if g == nil {
		g = &genGroup{}
		s.groups[closure] = g
	}
	return g
}

// safety: an on_failure node waits on its parent alone, as in waitsOn.
func (s *scheduler) generatedWaits(n nodeproto.PlanNode, waits []string) (_ []string, ready bool, err error) {
	if n.OnFailureOf != "" {
		return waits, true, nil
	}
	ready = true
	for _, id := range n.NeedsGenerated {
		switch g := s.groups[id]; {
		case g == nil || !g.done:
			ready = false
		case g.err != nil:
			return waits, true, g.err
		default:
			waits = append(waits, g.members...)
		}
	}
	return waits, ready, nil
}

func (s *scheduler) generators(id string) []string {
	var chain []string
	for closure := s.addedBy[id]; closure != ""; closure = s.addedBy[id] {
		chain = append([]string{closure}, chain...)
		id = s.sourceOf[id]
	}
	return chain
}

// safety: a memo hit's copy, outcome cached, adds its generators' nodes as its leader did.
func generatesNodes(n nodeproto.PlanNode, outcome sparkwing.Outcome) bool {
	return n.Closures != nil && len(n.Closures.Generator) > 0 && (outcome == sparkwing.Success || outcome == sparkwing.Cached)
}

type expansionFrom struct{ source, closure string }

func (f *expansionFrom) record(row *store.Node, n nodeproto.PlanNode, batch int) error {
	if f == nil {
		return nil
	}
	spec, err := json.Marshal(n)
	if err != nil {
		return err
	}
	row.Generated = &store.GeneratedNode{Source: f.source, Generator: f.closure, Batch: batch, Spec: spec}
	// safety: a generated recovery node loads its code with a generator that reads the source's output, which the
	// controller grants only to the source's descendants, so its row descends from its parent as the parent does
	// from the source; the scheduler still waits on the parent alone.
	if p := n.OnFailureOf; p != "" && !slices.Contains(row.Deps, p) {
		row.Deps = append(row.Deps, p)
	}
	return nil
}

type nodeLister interface {
	ListNodes(ctx context.Context, runID string) ([]*store.Node, error)
}

// safety: complete only when every node of the batch has its row, so a partial batch is never taken whole.
type recordedBatch struct {
	expansion
	complete bool
}

// safety: the expansion's membership is the rows the run recorded, so a later host adds every node the run
// accepted, finished or not, and never one a generator would return now. A generator with no batch recorded, as an
// empty one records nothing, or only part of one, as a host stopped mid-add leaves it, is incomplete.
func storedExpansions(ctx context.Context, st storage.StateStore, runID string, n nodeproto.PlanNode) ([]recordedBatch, error) {
	out := make([]recordedBatch, 0, len(n.Closures.Generator))
	var rows []*store.Node
	if lister, ok := st.(nodeLister); ok {
		var err error
		if rows, err = lister.ListNodes(context.WithoutCancel(ctx), runID); err != nil {
			return nil, fmt.Errorf("read the nodes %s generated: %w", n.ID, err)
		}
	}
	for _, closure := range n.Closures.Generator {
		b := recordedBatch{expansion: expansion{closure: closure}}
		size := 0
		for _, row := range rows {
			g := row.Generated
			if g == nil || g.Source != n.ID || g.Generator != closure {
				continue
			}
			var node nodeproto.PlanNode
			if err := json.Unmarshal(g.Spec, &node); err != nil {
				return nil, fmt.Errorf("read generated node %s: %w", row.NodeID, err)
			}
			b.nodes, size = append(b.nodes, node), g.Batch
		}
		b.complete = len(b.nodes) > 0 && len(b.nodes) == size
		out = append(out, b)
	}
	return out, nil
}

func allRecorded(batches []recordedBatch) bool {
	return !slices.ContainsFunc(batches, func(b recordedBatch) bool { return !b.complete })
}

// safety: each generator's batch stands on its own: a complete one is added as recorded, and only an incomplete
// one is generated, once, from the source's recorded output in an eval process, keeping every node it recorded.
func (s *scheduler) recordedExpansions(ctx context.Context, n nodeproto.PlanNode, batches []recordedBatch, output func() (json.RawMessage, error)) []expansion {
	var regenerated []expansion
	var genErr error
	if !allRecorded(batches) {
		var out json.RawMessage
		switch {
		case s.regenerate == nil:
			genErr = fmt.Errorf("the nodes %s generated are not all recorded, and this host has no eval process to generate them", n.ID)
		default:
			if out, genErr = output(); genErr == nil {
				regenerated = s.regenerate(ctx, &NodeRun{Run: s.run, Plan: n, Generators: s.generators(n.ID)}, out)
			}
		}
	}
	result := make([]expansion, 0, len(batches))
	for _, b := range batches {
		if b.complete {
			result = append(result, b.expansion)
			continue
		}
		result = append(result, reconcileBatch(b, regenerated, genErr))
	}
	return result
}

// safety: a recorded node stays a member, so a regenerated batch that drops or changes one is plan drift, never a
// smaller fan-out.
func reconcileBatch(b recordedBatch, regenerated []expansion, genErr error) expansion {
	e := expansion{closure: b.closure, err: genErr}
	j := slices.IndexFunc(regenerated, func(r expansion) bool { return r.closure == b.closure })
	switch {
	case genErr != nil:
		return e
	case j < 0:
		e.err = fmt.Errorf("generator %s did not run", b.closure)
		return e
	case regenerated[j].err != nil:
		e.err = regenerated[j].err
		return e
	}
	for _, rec := range b.nodes {
		k := slices.IndexFunc(regenerated[j].nodes, func(got nodeproto.PlanNode) bool { return got.ID == rec.ID })
		if k < 0 || regenerated[j].nodes[k].SpecHash != rec.SpecHash {
			e.err = &nodeproto.Error{Code: nodeproto.CodePlanDrift, Message: fmt.Sprintf(
				"generator %s no longer returns node %s as the run recorded it; a generator must return the same nodes for the same source output",
				b.closure, rec.ID)}
			return e
		}
	}
	e.nodes = regenerated[j].nodes
	return e
}
