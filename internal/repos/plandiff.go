package repos

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// PlanStep is one work item (step / job spawn) inside a plan node,
// reduced to the fields whose change alters pipeline behavior.
type PlanStep struct {
	ID       string   `json:"id"`
	Needs    []string `json:"needs,omitempty"`
	Decision string   `json:"decision,omitempty"`
}

// PlanNode is one node in a plan, reduced to structural fields.
type PlanNode struct {
	ID          string     `json:"id"`
	Deps        []string   `json:"deps,omitempty"`
	Decision    string     `json:"decision,omitempty"`
	IsApproval  bool       `json:"is_approval,omitempty"`
	OnFailureOf string     `json:"on_failure_of,omitempty"`
	Steps       []PlanStep `json:"steps,omitempty"`
}

// Plan is the structural projection of a pipeline's plan preview used
// for before/after comparison: the parts that describe the DAG's shape
// and each node's run decision, with runtime-volatile detail dropped.
type Plan struct {
	Pipeline string     `json:"pipeline"`
	Nodes    []PlanNode `json:"nodes"`
}

// canonical returns the plan with node and dependency ordering
// normalized so hashing and comparison ignore incidental ordering.
func (p Plan) canonical() Plan {
	nodes := make([]PlanNode, len(p.Nodes))
	copy(nodes, p.Nodes)
	for i := range nodes {
		nodes[i].Deps = sortedCopy(nodes[i].Deps)
		steps := make([]PlanStep, len(nodes[i].Steps))
		copy(steps, nodes[i].Steps)
		for j := range steps {
			steps[j].Needs = sortedCopy(steps[j].Needs)
		}
		sort.Slice(steps, func(a, b int) bool { return steps[a].ID < steps[b].ID })
		nodes[i].Steps = steps
	}
	sort.Slice(nodes, func(a, b int) bool { return nodes[a].ID < nodes[b].ID })
	return Plan{Pipeline: p.Pipeline, Nodes: nodes}
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// Hash is a fast-path fingerprint of the plan's canonical structure.
// Equal hashes mean an identical plan; the structural diff is only
// computed when hashes differ.
func (p Plan) Hash() string {
	c := p.canonical()
	var b strings.Builder
	for _, n := range c.Nodes {
		fmt.Fprintf(&b, "N|%s|d=%s|a=%t|f=%s\n", n.ID, strings.Join(n.Deps, ","), n.IsApproval, n.OnFailureOf)
		fmt.Fprintf(&b, "  dec=%s\n", n.Decision)
		for _, s := range n.Steps {
			fmt.Fprintf(&b, "  S|%s|n=%s|dec=%s\n", s.ID, strings.Join(s.Needs, ","), s.Decision)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// NodeChange records how one node differs between two plans.
type NodeChange struct {
	ID      string
	Details []string
}

// PlanDiff is the structured comparison of two plans: which nodes were
// added, removed, or changed (deps, decision, approval/recovery role,
// or steps). Identical is true when nothing changed.
type PlanDiff struct {
	Identical    bool
	AddedNodes   []string
	RemovedNodes []string
	ChangedNodes []NodeChange
}

// DiffPlans compares before and after structurally. It short-circuits
// to Identical when the canonical hashes match, otherwise reports the
// node-level changes.
func DiffPlans(before, after Plan) PlanDiff {
	if before.Hash() == after.Hash() {
		return PlanDiff{Identical: true}
	}
	b := indexNodes(before)
	a := indexNodes(after)
	var d PlanDiff
	for id := range a {
		if _, ok := b[id]; !ok {
			d.AddedNodes = append(d.AddedNodes, id)
		}
	}
	for id := range b {
		if _, ok := a[id]; !ok {
			d.RemovedNodes = append(d.RemovedNodes, id)
		}
	}
	for id, bn := range b {
		an, ok := a[id]
		if !ok {
			continue
		}
		if details := nodeDelta(bn, an); len(details) > 0 {
			d.ChangedNodes = append(d.ChangedNodes, NodeChange{ID: id, Details: details})
		}
	}
	sort.Strings(d.AddedNodes)
	sort.Strings(d.RemovedNodes)
	sort.Slice(d.ChangedNodes, func(i, j int) bool { return d.ChangedNodes[i].ID < d.ChangedNodes[j].ID })
	d.Identical = len(d.AddedNodes) == 0 && len(d.RemovedNodes) == 0 && len(d.ChangedNodes) == 0
	return d
}

func indexNodes(p Plan) map[string]PlanNode {
	c := p.canonical()
	m := make(map[string]PlanNode, len(c.Nodes))
	for _, n := range c.Nodes {
		m[n.ID] = n
	}
	return m
}

// nodeDelta lists the human-readable ways two same-id nodes differ.
func nodeDelta(before, after PlanNode) []string {
	var out []string
	if strings.Join(before.Deps, ",") != strings.Join(after.Deps, ",") {
		out = append(out, fmt.Sprintf("deps %s -> %s", bracket(before.Deps), bracket(after.Deps)))
	}
	if before.Decision != after.Decision {
		out = append(out, fmt.Sprintf("decision %s -> %s", orDash(before.Decision), orDash(after.Decision)))
	}
	if before.IsApproval != after.IsApproval {
		out = append(out, fmt.Sprintf("approval %t -> %t", before.IsApproval, after.IsApproval))
	}
	if before.OnFailureOf != after.OnFailureOf {
		out = append(out, fmt.Sprintf("on-failure-of %s -> %s", orDash(before.OnFailureOf), orDash(after.OnFailureOf)))
	}
	if sd := stepsDelta(before.Steps, after.Steps); len(sd) > 0 {
		out = append(out, sd...)
	}
	return out
}

func stepsDelta(before, after []PlanStep) []string {
	bi := map[string]PlanStep{}
	for _, s := range before {
		bi[s.ID] = s
	}
	ai := map[string]PlanStep{}
	for _, s := range after {
		ai[s.ID] = s
	}
	var out []string
	var added, removed []string
	for id := range ai {
		if _, ok := bi[id]; !ok {
			added = append(added, id)
		}
	}
	for id := range bi {
		if _, ok := ai[id]; !ok {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	for _, id := range added {
		out = append(out, "step +"+id)
	}
	for _, id := range removed {
		out = append(out, "step -"+id)
	}
	var changed []string
	for id, bs := range bi {
		as, ok := ai[id]
		if !ok {
			continue
		}
		if bs.Decision != as.Decision {
			changed = append(changed, fmt.Sprintf("step %s decision %s -> %s", id, orDash(bs.Decision), orDash(as.Decision)))
		} else if strings.Join(bs.Needs, ",") != strings.Join(as.Needs, ",") {
			changed = append(changed, fmt.Sprintf("step %s needs %s -> %s", id, bracket(bs.Needs), bracket(as.Needs)))
		}
	}
	sort.Strings(changed)
	return append(out, changed...)
}

func bracket(in []string) string {
	if len(in) == 0 {
		return "[]"
	}
	return "[" + strings.Join(in, ",") + "]"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
