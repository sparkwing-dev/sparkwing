package orchestrator

import (
	"fmt"
	"path"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// computeOnlySkip resolves the --only job-level filter into a map of
// (node-id -> human-readable skip reason) consulted at runOneNode
// entry. The keep-set is:
//
//   - every node whose ID matches `pattern` under path.Match, plus
//   - every transitive Needs() ancestor of a matched node.
//
// Ancestors are pulled in so a glob hitting a leaf still produces a
// self-consistent dispatch: the leaf's preconditions execute (or
// cache-hit) instead of failing with "unknown dependency."
//
// Returns ErrOnlyNoMatch when the pattern is non-empty but no node
// matches; the caller surfaces this at run start so a typo doesn't
// silently no-op into "everything skipped."
//
// Empty pattern returns (nil, nil): no filter applies.
func computeOnlySkip(plan *sparkwing.Plan, pattern string) (map[string]string, error) {
	if plan == nil || pattern == "" {
		return nil, nil
	}
	nodes := plan.Nodes()
	if len(nodes) == 0 {
		return nil, nil
	}

	byID := make(map[string]*sparkwing.JobNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID()] = n
	}

	matched, err := matchNodes(nodes, pattern)
	if err != nil {
		return nil, err
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("--only=%q matched no jobs (declared: %v)",
			pattern, declaredJobIDs(nodes))
	}

	keep := make(map[string]bool, len(matched))
	for id := range matched {
		expandAncestors(byID, id, keep)
	}

	out := make(map[string]string, len(nodes)-len(keep))
	for _, n := range nodes {
		if keep[n.ID()] {
			continue
		}
		out[n.ID()] = fmt.Sprintf("outside --only=%q", pattern)
	}
	return out, nil
}

// matchNodes returns the subset of nodes whose ID satisfies the
// path.Match glob. A malformed pattern surfaces as path.ErrBadPattern
// -- the caller wraps that with the offending input.
func matchNodes(nodes []*sparkwing.JobNode, pattern string) (map[string]bool, error) {
	out := make(map[string]bool)
	for _, n := range nodes {
		ok, err := path.Match(pattern, n.ID())
		if err != nil {
			return nil, fmt.Errorf("--only=%q: %w", pattern, err)
		}
		if ok {
			out[n.ID()] = true
		}
	}
	return out, nil
}

// expandAncestors adds id and every transitive Needs() ancestor
// reachable through plan-known nodes to keep. Stops at unknown ids
// (dynamic-group members not yet expanded) -- they participate via
// their parent's keep-status.
func expandAncestors(byID map[string]*sparkwing.JobNode, id string, keep map[string]bool) {
	if keep[id] {
		return
	}
	n, ok := byID[id]
	if !ok {
		return
	}
	keep[id] = true
	for _, dep := range n.DepIDs() {
		expandAncestors(byID, dep, keep)
	}
}

func declaredJobIDs(nodes []*sparkwing.JobNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID())
	}
	return out
}
