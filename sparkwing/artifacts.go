package sparkwing

import (
	"slices"
)

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
// Consuming a producer that declared no [JobNode.Artifacts] is a
// plan-time error. Repeating Consumes for the same producer overwrites
// the prior edge (last call wins), so the staging options are taken
// from the final call.
//
//	build := sw.Job(plan, "build", &Build{}).Artifacts("dist/**")
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
