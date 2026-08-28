package orchestrator

import (
	"fmt"
	"path"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
