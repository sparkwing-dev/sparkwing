package sparkwingruntime

import (
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Closure kinds, as the plan document names them under closures.
const (
	ClosureSkipIf    = "skip_if"
	ClosureBeforeRun = "before_run"
	ClosureAfterRun  = "after_run"
	ClosureVerify    = "verify"
	ClosureCacheKey  = "cache_key"
	ClosureGenerator = "generator"
)

// Closure is one piece of customer code the host asks for by id.
type Closure struct {
	Kind string
	Node *sparkwing.JobNode
	// Step is set only for a step's skip_if.
	Step *sparkwing.WorkStep
	// Fn is a [sparkwing.SkipPredicate], [sparkwing.BeforeRunFn],
	// [sparkwing.AfterRunFn], [sparkwing.VerifyFn], [sparkwing.CacheKeyFn] or
	// [sparkwing.ExpandGenerator], matching Kind.
	Fn any
}

// safety: node and step ids may hold '/', so each is escaped into one segment;
// the first segment then decodes to the node, and the second is a kind or
// "step", which keeps ids from distinct nodes and steps apart.
var closureIDSegment = strings.NewReplacer("%", "%25", "/", "%2F")

func closureID(node string, step *sparkwing.WorkStep, kind string, i int) string {
	id := closureIDSegment.Replace(node)
	if step != nil {
		id += "/step/" + closureIDSegment.Replace(step.ID())
	}
	return id + "/" + kind + "/" + strconv.Itoa(i)
}

// safety: the one walk both the plan document and LookupClosure use, so an
// emitted id always resolves to the closure it was assigned to.
func eachClosure(p *sparkwing.Plan, n *sparkwing.JobNode, visit func(id string, c Closure)) {
	add := func(step *sparkwing.WorkStep, kind string, i int, fn any) {
		visit(closureID(n.ID(), step, kind, i), Closure{Kind: kind, Node: n, Step: step, Fn: fn})
	}
	for i, fn := range n.SkipPredicates() {
		add(nil, ClosureSkipIf, i, fn)
	}
	for i, fn := range n.BeforeRunHooks() {
		add(nil, ClosureBeforeRun, i, fn)
	}
	for i, fn := range n.AfterRunHooks() {
		add(nil, ClosureAfterRun, i, fn)
	}
	if fn := n.Verifier(); fn != nil {
		add(nil, ClosureVerify, 0, fn)
	}
	if cfg := n.MemoizeConfig(); cfg != nil && cfg.Key != nil {
		add(nil, ClosureCacheKey, 0, cfg.Key)
	}
	generators := 0
	for _, exp := range p.Expansions() {
		if exp.Source == n {
			add(nil, ClosureGenerator, generators, exp.Gen)
			generators++
		}
	}
	if w := n.Work(); w != nil {
		for _, s := range w.Steps() {
			for i, fn := range s.SkipPredicates() {
				add(s, ClosureSkipIf, i, fn)
			}
		}
	}
}

// NodeClosureIDs returns the closure ids of n and of each of its steps, keyed
// by step id; either is nil or absent when it has no closures.
func NodeClosureIDs(p *sparkwing.Plan, n *sparkwing.JobNode) (*nodeproto.PlanClosures, map[string]*nodeproto.PlanClosures) {
	var node *nodeproto.PlanClosures
	steps := map[string]*nodeproto.PlanClosures{}
	eachClosure(p, n, func(id string, c Closure) {
		if c.Step == nil {
			node = appendClosureID(node, c.Kind, id)
			return
		}
		steps[c.Step.ID()] = appendClosureID(steps[c.Step.ID()], c.Kind, id)
	})
	return node, steps
}

func appendClosureID(ids *nodeproto.PlanClosures, kind, id string) *nodeproto.PlanClosures {
	if ids == nil {
		ids = &nodeproto.PlanClosures{}
	}
	switch kind {
	case ClosureSkipIf:
		ids.SkipIf = append(ids.SkipIf, id)
	case ClosureBeforeRun:
		ids.BeforeRun = append(ids.BeforeRun, id)
	case ClosureAfterRun:
		ids.AfterRun = append(ids.AfterRun, id)
	case ClosureVerify:
		ids.Verify = id
	case ClosureCacheKey:
		ids.CacheKey = id
	case ClosureGenerator:
		ids.Generator = append(ids.Generator, id)
	}
	return ids
}

// LookupClosure finds the closure with the given id among the plan's nodes,
// their OnFailure recovery nodes and every node a generator has added so far.
func LookupClosure(p *sparkwing.Plan, id string) (Closure, bool) {
	var found Closure
	ok := false
	seen := map[string]bool{}
	walk := func(n *sparkwing.JobNode) {
		if n == nil || seen[n.ID()] || ok {
			return
		}
		seen[n.ID()] = true
		eachClosure(p, n, func(cid string, c Closure) {
			if !ok && cid == id {
				found, ok = c, true
			}
		})
	}
	nodes := p.Nodes()
	for _, n := range nodes {
		walk(n)
	}
	for _, n := range nodes {
		walk(n.OnFailureNode())
	}
	return found, ok
}
