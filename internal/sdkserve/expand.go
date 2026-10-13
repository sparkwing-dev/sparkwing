package sdkserve

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Expand evaluates the generator closure with the given id under ctx, adds the
// nodes it returns to the planned plan so later requests in this process can
// name them, and answers their node documents as the eval value. A generator
// that panics, or returns nodes the plan cannot take, is the closure's error.
// A failed reply means the closure is not a generator of this plan, or the
// node documents could not be built.
func Expand(ctx context.Context, cfg Config, planned *Planned, closure string) (nodeproto.EvalResult, error) {
	c, ok := sparkwingruntime.LookupClosure(planned.Plan, closure)
	if !ok || c.Kind != sparkwingruntime.ClosureGenerator {
		return nodeproto.EvalResult{}, &nodeproto.Error{Code: nodeproto.CodeUnknownClosure, Message: fmt.Sprintf("no generator %q in this process's plan", closure)}
	}
	exp, ok := expansion(planned.Plan, c.Node, closure)
	if !ok {
		return nodeproto.EvalResult{}, fmt.Errorf("generator %q has no expansion", closure)
	}
	select {
	case <-exp.Group.Ready():
		return nodeproto.EvalResult{}, &nodeproto.Error{Code: nodeproto.CodeBadRequest, Message: fmt.Sprintf("generator %q already ran in this process", closure)}
	default:
	}
	children, err := generate(ctx, exp)
	if err == nil {
		err = sparkwing.RuntimePlumbing.Fns.PlanInsertExpanded(planned.Plan, exp.Source, children)
	}
	if err != nil {
		sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(exp.Group, nil, err)
		return closureFailed(err), nil
	}
	sparkwing.RuntimePlumbing.Fns.JobGroupFinalize(exp.Group, children, nil)

	doc, err := cfg.PlanDoc(planned.Plan, planned.Run, planned.Registration)
	if err != nil {
		return nodeproto.EvalResult{}, fmt.Errorf("plan document after %s: %w", closure, err)
	}
	byID := make(map[string]nodeproto.PlanNode, len(doc.Nodes))
	for _, n := range doc.Nodes {
		byID[n.ID] = n
	}
	nodes := []nodeproto.PlanNode{}
	for _, child := range children {
		ids := []string{child.ID()}
		if rec := child.OnFailureNode(); rec != nil {
			ids = append(ids, rec.ID())
		}
		for _, id := range ids {
			n, ok := byID[id]
			if !ok {
				return nodeproto.EvalResult{}, fmt.Errorf("generated node %s is missing from the plan document", id)
			}
			nodes = append(nodes, n)
		}
	}
	value, err := json.Marshal(nodes)
	if err != nil {
		return nodeproto.EvalResult{}, err
	}
	return nodeproto.EvalResult{Value: value}, nil
}

// safety: generator ids number a node's expansions in plan order, the walk
// LookupClosure assigned them by, so the id's position finds its expansion.
func expansion(p *sparkwing.Plan, source *sparkwing.JobNode, closure string) (sparkwing.Expansion, bool) {
	ids, _ := sparkwingruntime.NodeClosureIDs(p, source)
	i := slices.Index(ids.Generator, closure)
	for _, exp := range p.Expansions() {
		if exp.Source != source {
			continue
		}
		if i == 0 {
			return exp, true
		}
		i--
	}
	return sparkwing.Expansion{}, false
}

func generate(ctx context.Context, exp sparkwing.Expansion) (children []*sparkwing.JobNode, err error) {
	defer func() {
		if p := recover(); p != nil {
			children, err = nil, fmt.Errorf("generator of %s panicked: %v", exp.Source.ID(), p)
		}
	}()
	return exp.Gen(ctx), nil
}

// hack: until the node routes serve other nodes' outputs, Ref.Get and a
// generator read only the output of a node that ran in this process, such as
// a source whose attempt process evaluates its generator.
func localOutput(planned *Planned, id string) ([]byte, bool) {
	n := planned.Plan.Job(id)
	if n == nil || n.ResultStep() == nil || n.ResultStep().Output() == nil {
		return nil, false
	}
	raw, err := json.Marshal(n.ResultStep().Output())
	return raw, err == nil
}
