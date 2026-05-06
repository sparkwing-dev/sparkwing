// Package api carries wire-shape helpers shared between the
// laptop-embedded controller (internal/local) and the cluster-mode
// controller (pkg/controller). Both packages serve the same
// /api/v1/* surface and need to agree on response shapes that aren't
// covered by raw store.* types alone -- this is where those shared
// shapes live.
package api

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// Decorations is the per-node plan-snapshot-derived render envelope.
// Every field is omitempty so a node with no decorations marshals
// to an empty object (or, when the wrapper opts not to attach one,
// is omitted entirely from the wrapped node response).
type Decorations struct {
	// Groups is every named *NodeGroup this node belongs to (declared
	// via sparkwing.GroupJobs(plan, name, members...)). Empty for
	// ungrouped nodes; the dashboard renders those flat.
	Groups []string `json:"groups,omitempty"`
	// Dynamic marks nodes whose downstream shape is runtime-variable
	// -- either explicit `.Dynamic()` or the source of an ExpandFrom.
	// The dashboard paints a rainbow "DYNAMIC" pill on these, matching
	// the terminal's rainbow-letter tag.
	Dynamic bool `json:"dynamic,omitempty"`
	// Approval marks nodes declared with sparkwing.JobApproval(...). The
	// dashboard always renders an approval pill on these -- grey
	// before the gate is reached, amber-pulsing while pending, solid
	// green/red once resolved -- so the gate is visible throughout
	// the run, not only while awaiting a human.
	Approval bool `json:"approval,omitempty"`
	// OnFailureOf carries the parent node ID when this node was
	// attached via .OnFailure(id, job). The DAG uses it to draw a
	// dashed failure-branch edge and to place the recovery node in
	// a column right of its parent instead of stranding it at level 0.
	OnFailureOf string `json:"on_failure_of,omitempty"`
	// Modifiers carries the node's active Plan-layer modifiers so the
	// dashboard can render the dispatch envelope (Retry / Timeout /
	// RunsOn / Cache / Inline / hook presence) inline with the node
	// card.
	Modifiers *NodeModifiers `json:"modifiers,omitempty"`
	// Work is the node's inner DAG: Steps with Needs plus SpawnNode
	// declarations. Populated for nodes registered via plan.Job.
	// Renderers walk this to draw the Plan -> Node -> Work -> Step
	// tree.
	Work *NodeWork `json:"work,omitempty"`
}

// NodeModifiers mirrors the orchestrator's snapshotModifiers wire
// shape locally so this package doesn't import the orchestrator.
// Kept lossless so the dashboard sees every label the explain CLI
// shows.
type NodeModifiers struct {
	Retry           int      `json:"retry,omitempty"`
	RetryBackoffMS  int64    `json:"retry_backoff_ms,omitempty"`
	RetryAuto       bool     `json:"retry_auto,omitempty"`
	TimeoutMS       int64    `json:"timeout_ms,omitempty"`
	RunsOn          []string `json:"runs_on,omitempty"`
	CacheKey        string   `json:"cache_key,omitempty"`
	CacheMax        int      `json:"cache_max,omitempty"`
	CacheOnLimit    string   `json:"cache_on_limit,omitempty"`
	Inline          bool     `json:"inline,omitempty"`
	Optional        bool     `json:"optional,omitempty"`
	ContinueOnError bool     `json:"continue_on_error,omitempty"`
	OnFailure       string   `json:"on_failure,omitempty"`
	HasBeforeRun    bool     `json:"has_before_run,omitempty"`
	HasAfterRun     bool     `json:"has_after_run,omitempty"`
	HasSkipIf       bool     `json:"has_skip_if,omitempty"`
}

// NodeWork is the inner-Work tree (Step + Spawn + SpawnEach
// declarations) the dashboard renders inside each node card.
type NodeWork struct {
	Steps      []NodeStep      `json:"steps,omitempty"`
	Spawns     []NodeSpawn     `json:"spawns,omitempty"`
	SpawnEach  []NodeSpawnEach `json:"spawn_each,omitempty"`
	ResultStep string          `json:"result_step,omitempty"`
}

type NodeStep struct {
	ID        string   `json:"id"`
	Needs     []string `json:"needs,omitempty"`
	IsResult  bool     `json:"is_result,omitempty"`
	HasSkipIf bool     `json:"has_skip_if,omitempty"`
}

type NodeSpawn struct {
	ID         string    `json:"id"`
	Needs      []string  `json:"needs,omitempty"`
	TargetJob  string    `json:"target_job,omitempty"`
	TargetWork *NodeWork `json:"target_work,omitempty"`
	HasSkipIf  bool      `json:"has_skip_if,omitempty"`
}

type NodeSpawnEach struct {
	ID               string    `json:"id"`
	Needs            []string  `json:"needs,omitempty"`
	TargetJob        string    `json:"target_job,omitempty"`
	ItemTemplateWork *NodeWork `json:"item_template_work,omitempty"`
	Note             string    `json:"note,omitempty"`
}

// NodeWithDecorations is the wrapped per-node response shape used on
// /api/v1/runs/{id}?include=nodes when a plan snapshot is available.
// The store.Node fields are inlined (the JSON marshaller flattens via
// the embedded pointer), and Decorations rides alongside under a
// nested object. Decorations is nil-omitted for nodes with no
// snapshot-derived adornments so the wire shape remains additive.
type NodeWithDecorations struct {
	*store.Node
	Decorations *Decorations `json:"decorations,omitempty"`
}

// DecorationsFromSnapshot pulls per-node decorations out of the
// stored plan-snapshot JSON. The snapshot is authored by
// marshalPlanSnapshot in pkg/orchestrator; we decode into a local
// shape rather than importing the orchestrator types. Returns nil
// for empty/unparseable snapshots -- callers treat that the same as
// "no decorations" and the dashboard falls back to rendering nodes
// without adornments.
func DecorationsFromSnapshot(snapshot []byte) map[string]*Decorations {
	if len(snapshot) == 0 {
		return nil
	}
	var parsed struct {
		Nodes []struct {
			ID       string   `json:"id"`
			Groups   []string `json:"groups"`
			Dynamic  bool     `json:"dynamic"`
			Approval *struct {
				Message string `json:"message"`
			} `json:"approval"`
			OnFailureOf string         `json:"on_failure_of"`
			Modifiers   *NodeModifiers `json:"modifiers"`
			Work        *NodeWork      `json:"work"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(snapshot, &parsed); err != nil {
		return nil
	}
	out := make(map[string]*Decorations, len(parsed.Nodes))
	for _, n := range parsed.Nodes {
		hasApproval := n.Approval != nil
		if len(n.Groups) == 0 && !n.Dynamic && !hasApproval && n.OnFailureOf == "" && n.Modifiers == nil && n.Work == nil {
			continue
		}
		out[n.ID] = &Decorations{
			Groups:      n.Groups,
			Dynamic:     n.Dynamic,
			Approval:    hasApproval,
			OnFailureOf: n.OnFailureOf,
			Modifiers:   n.Modifiers,
			Work:        n.Work,
		}
	}
	return out
}

// DecorateNodes wraps a slice of store.Node with their decorations.
// When snapshot is empty / unparseable, returns the original nodes
// (typed as []*NodeWithDecorations with nil Decorations) so callers
// still emit the wrapped wire shape consistently. Pass-through nil
// decorations marshal to the bare store.Node fields with no
// `"decorations"` key -- additive vs. the baseline.
func DecorateNodes(nodes []*store.Node, snapshot []byte) []*NodeWithDecorations {
	dmap := DecorationsFromSnapshot(snapshot)
	out := make([]*NodeWithDecorations, len(nodes))
	for i, n := range nodes {
		out[i] = &NodeWithDecorations{Node: n, Decorations: dmap[n.NodeID]}
	}
	return out
}
