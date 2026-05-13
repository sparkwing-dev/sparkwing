// Package api carries wire-shape helpers shared between the
// laptop-embedded controller (internal/local) and the cluster-mode
// controller (pkg/controller). Both packages serve the same
// /api/v1/* surface and need to agree on response shapes that aren't
// covered by raw store.* types alone -- this is where those shared
// shapes live.
package api

import (
	"encoding/json"
	"time"

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
	// ApprovalState carries the runtime resolution of an approval
	// gate: who approved (or "auto-approved by timeout policy"), the
	// optional human comment, when it was resolved, and when it was
	// originally requested. Populated for nodes with Approval=true
	// when an approvals row exists; nil otherwise. The dashboard
	// renders this on hover so an operator sees the answer without
	// digging through the approvals API.
	ApprovalState *NodeApprovalState `json:"approval_state,omitempty"`
	// Work is the node's inner DAG: Steps with Needs plus SpawnNode
	// declarations. Populated for nodes registered via plan.Job.
	// Renderers walk this to draw the Plan -> Node -> Work -> Step
	// tree.
	Work *NodeWork `json:"work,omitempty"`
	// SpawnedPipelines is the list of cross-pipeline calls a node
	// fired via sparkwing.RunAndAwait during its body. Joined from
	// the triggers table at response time (each child trigger carries
	// parent_node_id + pipeline). Empty for nodes that didn't spawn
	// any cross-pipeline runs. The dashboard renders a corner pill
	// listing the targets so cross-pipeline edges are visible without
	// drilling into the trigger log.
	SpawnedPipelines []SpawnedPipelineRef `json:"spawned_pipelines,omitempty"`
}

// SpawnedPipelineRef is one cross-pipeline call out of a node: the
// target pipeline name and the child run id the awaiter created so
// the dashboard can deep-link from the pill into the spawned run.
type SpawnedPipelineRef struct {
	Pipeline   string `json:"pipeline"`
	ChildRunID string `json:"child_run_id"`
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
	StepGroups []NodeStepGroup `json:"step_groups,omitempty"`
	ResultStep string          `json:"result_step,omitempty"`
}

// NodeStepGroup is one sparkwing.GroupSteps declaration: a named
// bundle of step IDs the dashboard frames as a cluster inside the
// inner Work DAG. Step-to-group membership is computed client-side
// by intersecting Members against the surrounding Steps array,
// mirroring how the node-group renderer works at the Plan layer.
type NodeStepGroup struct {
	Name    string   `json:"name,omitempty"`
	Members []string `json:"members"`
}

type NodeStep struct {
	ID        string   `json:"id"`
	Needs     []string `json:"needs,omitempty"`
	IsResult  bool     `json:"is_result,omitempty"`
	HasSkipIf bool     `json:"has_skip_if,omitempty"`

	// Runtime state, joined in from node_steps rows. Populated when
	// the response handler hands a step lookup to DecorateNodes (the
	// dashboard's /runs/{id}?include=nodes path). Status is one of
	// "running" | "passed" | "failed" | "skipped"; empty means the
	// step hasn't started (or step state isn't being tracked yet for
	// this run).
	Status      string     `json:"status,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	DurationMS  int64      `json:"duration_ms,omitempty"`
	Annotations []string   `json:"annotations,omitempty"`
	// Latest markdown summary posted by sparkwing.Summary during this
	// step. Overwrite-on-write (unlike Annotations), so only the most
	// recent call survives. Empty when no Summary was emitted.
	Summary string `json:"summary,omitempty"`
}

// NodeApprovalState is the runtime resolution of a JobApproval gate.
// Fields mirror the approvals store row but trimmed to what the
// dashboard's hover card surfaces. Empty Resolution means the gate
// is still pending (requested but unresolved); Approver=="sparkwing"
// signals an orchestrator-written timeout resolution rather than a
// human action.
type NodeApprovalState struct {
	Resolution  string     `json:"resolution,omitempty"`
	Approver    string     `json:"approver,omitempty"`
	Comment     string     `json:"comment,omitempty"`
	RequestedAt time.Time  `json:"requested_at,omitempty"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	TimeoutMS   int64      `json:"timeout_ms,omitempty"`
	OnTimeout   string     `json:"on_timeout,omitempty"`
	Message     string     `json:"message,omitempty"`
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
//
// steps, when non-nil, joins runtime per-step state into each
// node's Decorations.Work.Steps tree. Pass nil to skip the join
// (e.g. lightweight list endpoints that don't render the inner DAG).
//
// approvals, when non-nil, joins approval-gate resolutions (who
// approved, when, via what path) onto each gate node's Decorations
// .ApprovalState. Pass nil to skip the join.
//
// spawned, when non-nil, joins cross-pipeline spawn records (one row
// per RunAndAwait invocation) onto the parent node's Decorations
// .SpawnedPipelines so the dashboard can render a corner pill.
func DecorateNodes(nodes []*store.Node, snapshot []byte, steps []*store.NodeStep, approvals []*store.Approval, spawned []store.SpawnedChild) []*NodeWithDecorations {
	dmap := DecorationsFromSnapshot(snapshot)
	if len(steps) > 0 {
		populateStepRuntime(dmap, steps)
	}
	if len(approvals) > 0 {
		populateApprovalState(dmap, approvals)
	}
	if len(spawned) > 0 {
		populateSpawnedPipelines(dmap, spawned)
	}
	out := make([]*NodeWithDecorations, len(nodes))
	for i, n := range nodes {
		out[i] = &NodeWithDecorations{Node: n, Decorations: dmap[n.NodeID]}
	}
	return out
}

// populateSpawnedPipelines attaches each SpawnedChild row to its
// parent node's Decorations. Lazily creates a Decorations entry for
// nodes whose snapshot carried no decoration so a runtime-spawned
// pipeline isn't silently dropped from the wire shape.
func populateSpawnedPipelines(dmap map[string]*Decorations, spawned []store.SpawnedChild) {
	for _, c := range spawned {
		if c.ParentNodeID == "" {
			continue
		}
		dec := dmap[c.ParentNodeID]
		if dec == nil {
			dec = &Decorations{}
			dmap[c.ParentNodeID] = dec
		}
		dec.SpawnedPipelines = append(dec.SpawnedPipelines, SpawnedPipelineRef{
			Pipeline:   c.Pipeline,
			ChildRunID: c.ChildRunID,
		})
	}
}

// populateApprovalState attaches each Approval row to its node's
// Decorations. Lazily creates a Decorations entry for nodes whose
// snapshot carried no decoration (otherwise we'd silently drop the
// runtime state on a node the snapshot didn't pre-decorate).
func populateApprovalState(dmap map[string]*Decorations, approvals []*store.Approval) {
	for _, a := range approvals {
		if a == nil {
			continue
		}
		dec := dmap[a.NodeID]
		if dec == nil {
			dec = &Decorations{}
			dmap[a.NodeID] = dec
		}
		dec.ApprovalState = &NodeApprovalState{
			Resolution:  a.Resolution,
			Approver:    a.Approver,
			Comment:     a.Comment,
			RequestedAt: a.RequestedAt,
			ResolvedAt:  a.ResolvedAt,
			TimeoutMS:   a.TimeoutMS,
			OnTimeout:   a.OnTimeout,
			Message:     a.Message,
		}
	}
}

// populateStepRuntime stamps each NodeStep with its runtime state
// from node_steps rows. Walks the entire Work tree (including spawn
// target_work and spawn_each item_template_work) so steps nested
// inside spawned sub-jobs get populated too.
func populateStepRuntime(dmap map[string]*Decorations, steps []*store.NodeStep) {
	byNode := make(map[string]map[string]*store.NodeStep, len(steps))
	for _, s := range steps {
		m, ok := byNode[s.NodeID]
		if !ok {
			m = make(map[string]*store.NodeStep)
			byNode[s.NodeID] = m
		}
		m[s.StepID] = s
	}
	for nodeID, dec := range dmap {
		lookup := byNode[nodeID]
		if dec == nil || dec.Work == nil || len(lookup) == 0 {
			continue
		}
		stampWork(dec.Work, lookup)
	}
}

func stampWork(w *NodeWork, lookup map[string]*store.NodeStep) {
	for i := range w.Steps {
		s := &w.Steps[i]
		row := lookup[s.ID]
		if row == nil {
			continue
		}
		s.Status = row.Status
		s.StartedAt = row.StartedAt
		s.FinishedAt = row.FinishedAt
		if row.StartedAt != nil && row.FinishedAt != nil {
			s.DurationMS = row.FinishedAt.Sub(*row.StartedAt).Milliseconds()
		}
		s.Annotations = row.Annotations
		s.Summary = row.Summary
	}
	for i := range w.Spawns {
		if w.Spawns[i].TargetWork != nil {
			stampWork(w.Spawns[i].TargetWork, lookup)
		}
	}
	for i := range w.SpawnEach {
		if w.SpawnEach[i].ItemTemplateWork != nil {
			stampWork(w.SpawnEach[i].ItemTemplateWork, lookup)
		}
	}
}
