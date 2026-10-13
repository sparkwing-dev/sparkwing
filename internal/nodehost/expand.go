package nodehost

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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

// safety: a generated node is in no process's plan until its generator runs
// there, so its attempt rebuilds it and compares it with what the run accepted.
func regenerate(ctx context.Context, sess *Session, n *NodeRun) error {
	nodes, err := evalGenerator(ctx, sess, n.Generator)
	if err != nil {
		return err
	}
	for _, got := range nodes {
		if got.ID != n.Plan.ID {
			continue
		}
		if got.SpecHash != n.Plan.SpecHash {
			return &nodeproto.Error{Code: nodeproto.CodePlanDrift, Message: fmt.Sprintf(
				"generator %s builds node %s differently than when the run added it; a generator must return the same nodes for the same source output", n.Generator, n.Plan.ID)}
		}
		return nil
	}
	return &nodeproto.Error{Code: nodeproto.CodePlanDrift, Message: fmt.Sprintf(
		"generator %s no longer returns node %s; a generator must return the same nodes for the same source output", n.Generator, n.Plan.ID)}
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
	if err := s.add(ctx, context.WithoutCancel(ctx), e.nodes); err != nil {
		return err
	}
	if s.addedBy == nil {
		s.addedBy = map[string]string{}
	}
	g := s.group(e.closure)
	for _, n := range e.nodes {
		s.addedBy[n.ID] = e.closure
		if n.OnFailureOf == "" {
			g.members = append(g.members, n.ID)
		}
	}
	s.run.Logger.Info("nodehost: expansion generated", "run", s.run.ID, "source", source, "generator", e.closure, "nodes", len(e.nodes))
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
