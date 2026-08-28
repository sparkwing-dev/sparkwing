package sparkwing

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// Outputs declares the files this node emits as artifacts, by glob,
// relative to its working directory. Repeatable; the union of every
// call is captured. A glob that matches nothing at run time records an
// empty set rather than failing the node -- some outputs are
// legitimately optional.
//
// Declaring Outputs does not require Cache: a node may publish
// artifacts for its consumers every run without being memoized, and
// memoization does not change how artifacts flow. A consumer stages a
// producer's artifacts by declaring [JobNode.Consumes].
//
//	build := sw.Job(plan, "build", &Build{}).Outputs("dist/**")
//	sw.Job(plan, "deploy", &Deploy{}).Consumes(build)
func (n *JobNode) Outputs(globs ...string) *JobNode {
	for _, g := range globs {
		if g == "" {
			continue
		}
		if !slices.Contains(n.outputGlobs, g) {
			n.outputGlobs = append(n.outputGlobs, g)
		}
	}
	return n
}

// OutputGlobs returns the artifact output globs declared via Outputs
// (the union across calls), or nil if the node declared none. Callers
// must not mutate the returned slice.
func (n *JobNode) OutputGlobs() []string {
	if len(n.outputGlobs) == 0 {
		return nil
	}
	out := make([]string, len(n.outputGlobs))
	copy(out, n.outputGlobs)
	return out
}

// ConsumeOption tunes a [JobNode.Consumes] declaration.
type ConsumeOption func(*consumeEdge)

type consumeEdge struct {
	producer string
	into     string
}

// Into stages the consumed producer's artifacts under prefix, with the
// producer's internal structure preserved. Without Into, the artifacts
// land at the producer's declared relative paths. The prefix applies to
// the whole producer; per-file remapping is intentionally not offered,
// since it would couple the consumer to the producer's filenames.
//
//	stage := sw.Job(plan, "stage", &Stage{}).
//	    Consumes(build, sparkwing.Into("artifacts/build"))
func Into(prefix string) ConsumeOption {
	return func(e *consumeEdge) { e.into = prefix }
}

// Consumes declares that this node stages the artifacts produced by
// producer into its workspace before it runs, and implies
// Needs(producer). By default the artifacts land at the producer's
// declared relative paths; pass [Into] to relocate them under a prefix
// with structure preserved.
//
// Consuming a producer that declared no [JobNode.Outputs] is a
// plan-time error. Repeating Consumes for the same producer overwrites
// the prior edge (last call wins), so the staging options are taken
// from the final call.
//
//	build := sw.Job(plan, "build", &Build{}).Outputs("dist/**")
//	sw.Job(plan, "deploy", &Deploy{}).Consumes(build)
//	sw.Job(plan, "archive", &Archive{}).
//	    Consumes(build, sparkwing.Into("artifacts/build"))
func (n *JobNode) Consumes(producer *JobNode, opts ...ConsumeOption) *JobNode {
	if producer == nil {
		return n
	}
	e := consumeEdge{producer: producer.id}
	for _, opt := range opts {
		if opt != nil {
			opt(&e)
		}
	}
	if i := slices.IndexFunc(n.consumes, func(c consumeEdge) bool { return c.producer == e.producer }); i >= 0 {
		n.consumes[i] = e
	} else {
		n.consumes = append(n.consumes, e)
	}
	n.Needs(producer)
	return n
}

// ConsumeEdge is a resolved consumer->producer artifact edge: the
// producer whose artifacts this node stages, and the staging prefix
// (empty stages at the producer's declared relative paths).
type ConsumeEdge struct {
	Producer string
	Into     string
}

// ConsumeEdges returns the artifact edges declared via Consumes, in
// declaration order, or nil if the node consumes nothing.
func (n *JobNode) ConsumeEdges() []ConsumeEdge {
	if len(n.consumes) == 0 {
		return nil
	}
	out := make([]ConsumeEdge, len(n.consumes))
	for i, e := range n.consumes {
		out[i] = ConsumeEdge{Producer: e.producer, Into: e.into}
	}
	return out
}

func (p *Plan) validateArtifactEdges() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, n := range p.nodes {
		for _, e := range n.consumes {
			prod := p.byID[e.producer]
			if prod == nil {
				continue
			}
			if len(prod.outputGlobs) == 0 {
				return fmt.Errorf(
					"sparkwing: node %q consumes %q, which declares no Outputs "+
						"(add %s.Outputs(...) on the producer, or drop the Consumes)",
					n.id, e.producer, e.producer)
			}
		}
	}

	for _, n := range p.nodes {
		p.lintWarnings = append(p.lintWarnings, artifactOverlapWarnings(n, p.byID)...)
	}
	return nil
}

func artifactOverlapWarnings(n *JobNode, byID map[string]*JobNode) []LintWarning {
	var out []LintWarning
	for i := 0; i < len(n.consumes); i++ {
		for j := i + 1; j < len(n.consumes); j++ {
			a, b := byID[n.consumes[i].producer], byID[n.consumes[j].producer]
			if a == nil || b == nil {
				continue
			}
			rootsA := stagingRoots(a.outputGlobs, n.consumes[i].into)
			rootsB := stagingRoots(b.outputGlobs, n.consumes[j].into)
			if hit, ok := firstOverlap(rootsA, rootsB); ok {
				out = append(out, LintWarning{
					NodeID: n.id,
					Code:   "artifact-stage-overlap",
					Msg: fmt.Sprintf(
						"consumes overlapping artifacts: producers %q and %q both stage to %q; the last staged wins",
						a.id, b.id, hit),
				})
			}
		}
	}
	return out
}

func stagingRoots(globs []string, into string) []string {
	out := make([]string, 0, len(globs))
	for _, g := range globs {
		root := path.Join(into, globLiteralPrefix(g))
		if root == "." {
			root = ""
		}
		if !slices.Contains(out, root) {
			out = append(out, root)
		}
	}
	return out
}

func globLiteralPrefix(glob string) string {
	var lit []string
	for _, seg := range strings.Split(glob, "/") {
		if strings.ContainsAny(seg, "*?[") {
			break
		}
		lit = append(lit, seg)
	}
	return strings.Join(lit, "/")
}

func firstOverlap(a, b []string) (string, bool) {
	for _, ra := range a {
		for _, rb := range b {
			if pathContains(ra, rb) {
				if len(ra) <= len(rb) {
					return ra, true
				}
				return rb, true
			}
		}
	}
	return "", false
}

func pathContains(a, b string) bool {
	a, b = path.Clean("/"+a), path.Clean("/"+b)
	if a == b || a == "/" || b == "/" {
		return true
	}
	return strings.HasPrefix(b+"/", a+"/") || strings.HasPrefix(a+"/", b+"/")
}
