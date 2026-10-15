package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// ScheduleNode is the immutable graph projection consumed by the shared decider.
// Status, outcome and expansion use the final schema's spellings. Claimed means
// a current holder exists; only the transition writer may judge its lease rights.
type ScheduleNode struct {
	ID              string
	Kind            string
	Status          string
	Outcome         string
	Generation      int64
	Ready           bool
	Claimed         bool
	Deps            []string
	DepsIfPresent   []string
	NeedsGenerated  []string
	OnFailureOf     string
	ContinueOnError bool
	Optional        bool
	Expansion       string
}

// ScheduleExpansion preserves generator identity even when its accepted member set is empty.
// Each declared generator has one row, with its source's persisted expansion state.
type ScheduleExpansion struct {
	GeneratorID  string
	SourceNodeID string
	State        string
	Members      []string
}

// ScheduleSnapshot is a stable read of the accepted graph and cancellation state.
// Nodes remain in accepted plan order, including persisted expansion insert order.
type ScheduleSnapshot struct {
	CancelRequested bool
	GuardRefusal    string
	Nodes           []ScheduleNode
	Expansions      []ScheduleExpansion
}

// DecisionKind separates graph intent from the store's transition authority.
type DecisionKind string

const (
	DecisionReady        DecisionKind = "ready"
	DecisionApproval     DecisionKind = "approval_pending"
	DecisionTerminal     DecisionKind = "terminal"
	DecisionCancelHolder DecisionKind = "cancel_holder"
	DecisionExpand       DecisionKind = "expand"
)

// NodeDecision is applied through TransitionNode, never by direct status updates.
type NodeDecision struct {
	NodeID      string
	Generation  int64
	GeneratorID string
	Kind        DecisionKind
	Outcome     string
	Reason      string
}

// ScheduleDecision contains all graph work plus an optional terminal run verdict.
// A missing verdict means unsettled, including any expected expansion.
type ScheduleDecision struct {
	Nodes   []NodeDecision
	Outcome string
	Reason  string
}

// Decide is the run host's entry to the same pure decision used in store settlement.
// It performs no I/O, samples no clock and mutates no input. Controller settlement
// applies its decisions inside the transition transaction and rechecks holder rights.
func Decide(snapshot ScheduleSnapshot) ScheduleDecision {
	nodes := slices.Clone(snapshot.Nodes)
	byID := make(map[string]int, len(nodes))
	groups := make(map[string]ScheduleExpansion, len(snapshot.Expansions))
	for index, node := range nodes {
		byID[node.ID] = index
	}
	for _, group := range snapshot.Expansions {
		groups[group.GeneratorID] = group
	}
	if snapshot.CancelRequested {
		return decideCancellation(nodes)
	}
	if snapshot.GuardRefusal != "" {
		return decideGuardRefusal(nodes, snapshot.GuardRefusal)
	}
	var result ScheduleDecision
	waiters := make(map[string][]int)
	remaining := make([]int, len(nodes))
	var queue []int
	for index, node := range nodes {
		if node.Status != "pending" || node.Claimed || node.Ready && node.Kind != "approval" {
			continue
		}
		deps, valid := scheduleDependencies(node, byID, groups)
		if !valid {
			continue
		}
		for _, id := range deps {
			if nodes[byID[id]].Status != "done" {
				remaining[index]++
				waiters[id] = append(waiters[id], index)
			}
		}
		if remaining[index] == 0 {
			queue = append(queue, index)
		}
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		index := queue[cursor]
		node := &nodes[index]
		decision := decideNode(*node, nodes, byID, groups)
		if decision.Kind == "" {
			continue
		}
		result.Nodes = append(result.Nodes, decision)
		if decision.Kind != DecisionTerminal {
			continue
		}
		node.Status, node.Outcome = "done", decision.Outcome
		if node.Expansion == "expected" {
			node.Expansion = "none"
			for id, group := range groups {
				if group.SourceNodeID == node.ID {
					group.State = "none"
					groups[id] = group
				}
			}
		}
		for _, dependent := range waiters[node.ID] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	for _, expansion := range snapshot.Expansions {
		source := nodes[byID[expansion.SourceNodeID]]
		if source.Status == "done" && source.Expansion == "expected" && scheduleSucceeded(source.Outcome) {
			result.Nodes = append(result.Nodes, NodeDecision{NodeID: source.ID, Generation: source.Generation, GeneratorID: expansion.GeneratorID, Kind: DecisionExpand})
		}
	}
	result.Outcome, result.Reason = scheduleRunVerdict(nodes)
	return result
}

func decideGuardRefusal(nodes []ScheduleNode, reason string) ScheduleDecision {
	result := ScheduleDecision{Outcome: "failed", Reason: reason}
	for _, node := range nodes {
		if node.Status == "done" {
			continue
		}
		if node.Claimed || node.Status == "running" {
			result.Outcome = ""
			continue
		}
		result.Nodes = append(result.Nodes, NodeDecision{NodeID: node.ID, Generation: node.Generation, Kind: DecisionTerminal, Outcome: "skipped", Reason: reason})
	}
	return result
}

func runGuardRefusal(run RunRow) string {
	if run.Spec == nil {
		return ""
	}
	guards := run.Spec.Resolved.Guards
	if guards.Passed || len(guards.When) == 0 && len(guards.Unless) == 0 && guards.Reason == "" {
		return ""
	}
	if guards.Reason != "" {
		return guards.Reason
	}
	return "run guard refused execution"
}

func decideCancellation(nodes []ScheduleNode) ScheduleDecision {
	result := ScheduleDecision{Outcome: "cancelled", Reason: "cancel_requested"}
	for _, node := range nodes {
		if node.Status == "done" {
			continue
		}
		kind := DecisionTerminal
		if node.Claimed || node.Status == "running" {
			kind = DecisionCancelHolder
			result.Outcome, result.Reason = "", ""
		}
		result.Nodes = append(result.Nodes, NodeDecision{NodeID: node.ID, Generation: node.Generation, Kind: kind, Outcome: "cancelled", Reason: "cancel_requested"})
	}
	return result
}

func scheduleDependencies(node ScheduleNode, byID map[string]int, groups map[string]ScheduleExpansion) ([]string, bool) {
	deps := slices.Clone(node.Deps)
	for _, id := range node.DepsIfPresent {
		if _, present := byID[id]; present {
			deps = append(deps, id)
		}
	}
	if node.OnFailureOf != "" {
		deps = append(deps, node.OnFailureOf)
	}
	for _, id := range node.NeedsGenerated {
		group, exists := groups[id]
		if !exists {
			return nil, false
		}
		deps = append(deps, group.SourceNodeID)
		deps = append(deps, group.Members...)
	}
	seen := make(map[string]bool, len(deps))
	unique := deps[:0]
	for _, id := range deps {
		if _, exists := byID[id]; !exists {
			return nil, false
		}
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	return unique, true
}

func decideNode(node ScheduleNode, nodes []ScheduleNode, byID map[string]int, groups map[string]ScheduleExpansion) NodeDecision {
	decision := NodeDecision{NodeID: node.ID, Generation: node.Generation}
	for _, id := range node.NeedsGenerated {
		if groups[id].State == "expected" {
			return decision
		}
	}
	if node.OnFailureOf != "" && nodes[byID[node.OnFailureOf]].Outcome != "failed" {
		decision.Kind, decision.Outcome, decision.Reason = DecisionTerminal, "skipped", "parent_did_not_fail"
		return decision
	}
	for _, id := range node.NeedsGenerated {
		group := groups[id]
		switch group.State {
		case "failed", "none":
			decision.Kind, decision.Outcome, decision.Reason = DecisionTerminal, "cancelled", "upstream_failed"
			if group.State == "none" && nodes[byID[group.SourceNodeID]].Outcome == "skipped" {
				decision.Outcome, decision.Reason = "skipped", "upstream_skipped"
			}
			return decision
		}
	}
	deps, _ := scheduleDependencies(node, byID, groups)
	for _, id := range deps {
		upstream := nodes[byID[id]]
		if id != node.OnFailureOf && !scheduleDependencyOK(upstream) {
			decision.Kind, decision.Outcome, decision.Reason = DecisionTerminal, "cancelled", "upstream_failed"
			return decision
		}
	}
	decision.Kind = DecisionReady
	if node.Kind == "approval" {
		decision.Kind = DecisionApproval
	}
	return decision
}

func scheduleSucceeded(outcome string) bool { return outcome == "success" || outcome == "memoized" }

func scheduleDependencyOK(node ScheduleNode) bool {
	return scheduleSucceeded(node.Outcome) || node.Outcome == "skipped" || node.ContinueOnError
}

func scheduleRunVerdict(nodes []ScheduleNode) (string, string) {
	var failed, superseded []string
	for _, node := range nodes {
		if node.Status != "done" || node.Expansion == "expected" {
			return "", ""
		}
		if node.Optional || node.OnFailureOf != "" {
			continue
		}
		switch {
		case node.Expansion == "failed", node.Outcome == "failed", node.Outcome == "cancelled":
			failed = append(failed, node.ID)
		case node.Outcome == "superseded":
			superseded = append(superseded, node.ID)
		}
	}
	if len(failed) != 0 {
		return "failed", "failed: " + strings.Join(failed, ", ")
	}
	if len(superseded) != 0 {
		return "cancelled", "superseded: " + strings.Join(superseded, ", ")
	}
	return "success", ""
}

// Scheduling projects one consistent store read into the shared decider input.
// Declarations and ordering come from accepted plan/spec bytes and expansion bodies.
// Generator IDs come from persisted ClosureSpec.Generate, and membership comes
// from accepted generated JobSpec.Groups. Every declared generator is retained,
// including accepted generators with no members. Missing or inconsistent
// declarations fail projection instead of synthesizing topology from live jobs.
// State comes only from persisted rows; elapsed leases never imply a requeue.
// Both run hosts and controller settlement call Decide on this projection.
func (snapshot RunSnapshot) Scheduling() (ScheduleSnapshot, error) {
	var plan nodeproto.PlanDoc
	if err := decodeScheduleDeclaration(snapshot.Run.Plan, &plan); err != nil {
		return ScheduleSnapshot{}, fmt.Errorf("accepted plan: %w", err)
	}
	if plan.Jobs == nil {
		return ScheduleSnapshot{}, fmt.Errorf("accepted plan has no jobs array")
	}
	projection := scheduleProjection{
		runID:    snapshot.Run.ID,
		result:   ScheduleSnapshot{CancelRequested: snapshot.Run.CancelRequestedAt != nil, GuardRefusal: runGuardRefusal(snapshot.Run)},
		rows:     make(map[string]NodeRow, len(snapshot.Nodes)),
		jobs:     make(map[string]nodeproto.JobSpec, len(snapshot.Nodes)),
		groups:   make(map[string]int),
		accepted: make(map[string]bool),
	}
	for _, row := range snapshot.Nodes {
		if row.Ref.RunID != snapshot.Run.ID {
			return ScheduleSnapshot{}, fmt.Errorf("job %q belongs to another run", row.Ref.NodeID)
		}
		if _, exists := projection.rows[row.Ref.NodeID]; exists {
			return ScheduleSnapshot{}, fmt.Errorf("duplicate persisted job %q", row.Ref.NodeID)
		}
		projection.rows[row.Ref.NodeID] = row
	}
	for _, job := range plan.Jobs {
		if len(job.Groups) != 0 {
			return ScheduleSnapshot{}, fmt.Errorf("initial job %q has generated membership", job.ID)
		}
		if err := projection.add(job); err != nil {
			return ScheduleSnapshot{}, err
		}
	}
	for _, expansion := range snapshot.Expansions {
		if err := projection.expand(expansion); err != nil {
			return ScheduleSnapshot{}, err
		}
	}
	if err := projection.validate(); err != nil {
		return ScheduleSnapshot{}, err
	}
	return projection.result, nil
}

type scheduleProjection struct {
	runID    string
	result   ScheduleSnapshot
	rows     map[string]NodeRow
	jobs     map[string]nodeproto.JobSpec
	groups   map[string]int
	accepted map[string]bool
}

func decodeScheduleDeclaration(raw json.RawMessage, value any) error {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("declaration must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("declaration contains trailing data")
	}
	return nil
}

func (projection *scheduleProjection) add(job nodeproto.JobSpec) error {
	if _, exists := projection.jobs[job.ID]; job.ID == "" || exists {
		return fmt.Errorf("empty or duplicate declared job %q", job.ID)
	}
	row, exists := projection.rows[job.ID]
	if !exists {
		return fmt.Errorf("declared job %q has no persisted row", job.ID)
	}
	var persisted nodeproto.JobSpec
	if err := decodeScheduleDeclaration(row.Spec, &persisted); err != nil {
		return fmt.Errorf("job %q spec: %w", job.ID, err)
	}
	expected, _ := json.Marshal(job)
	actual, _ := json.Marshal(persisted)
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("job %q differs from its accepted declaration", job.ID)
	}
	if err := validateScheduleState(row); err != nil {
		return fmt.Errorf("job %q: %w", job.ID, err)
	}
	projection.jobs[job.ID] = job
	kind := "work"
	if job.Approval != nil {
		kind = "approval"
	} else if row.Status == StatusApprovalPending {
		return fmt.Errorf("job %q waits for an undeclared approval", job.ID)
	}
	projection.result.Nodes = append(projection.result.Nodes, ScheduleNode{
		ID: job.ID, Kind: kind, Status: string(row.Status), Outcome: string(row.Outcome), Generation: row.Generation,
		Ready: row.Status == StatusPending && row.ClaimedBy == "" && row.ReadyAt != nil, Claimed: row.ClaimedBy != "", Deps: slices.Clone(job.Needs), DepsIfPresent: slices.Clone(job.NeedsIfPresent),
		NeedsGenerated: slices.Clone(job.NeedsGenerated), OnFailureOf: job.OnFailureOf, ContinueOnError: job.ContinueOnError, Optional: job.Optional, Expansion: string(row.Expansion),
	})
	for _, generator := range job.Closures.Generate {
		if _, exists := projection.groups[generator]; generator == "" || exists {
			return fmt.Errorf("empty or duplicate generator %q", generator)
		}
		projection.groups[generator] = len(projection.result.Expansions)
		projection.result.Expansions = append(projection.result.Expansions, ScheduleExpansion{GeneratorID: generator, SourceNodeID: job.ID, State: string(row.Expansion)})
	}
	return nil
}

func (projection *scheduleProjection) expand(expansion AcceptedExpansionRow) error {
	source, exists := projection.jobs[expansion.Source.NodeID]
	row := projection.rows[source.ID]
	if !exists || expansion.Source.RunID != projection.runID || projection.accepted[source.ID] || len(source.Closures.Generate) == 0 || row.Expansion != ExpansionAccepted || row.Generation != expansion.SourceGeneration {
		return fmt.Errorf("expansion source %q disagrees with persisted declaration or generation", expansion.Source.NodeID)
	}
	var body struct {
		Jobs []nodeproto.JobSpec `json:"jobs"`
	}
	if err := decodeScheduleDeclaration(expansion.Body, &body); err != nil {
		return fmt.Errorf("expansion source %q: %w", source.ID, err)
	}
	if body.Jobs == nil {
		return fmt.Errorf("expansion source %q has no jobs array", source.ID)
	}
	for _, job := range body.Jobs {
		if len(job.Groups) != 1 || !slices.Contains(source.Closures.Generate, job.Groups[0]) {
			return fmt.Errorf("generated job %q has no unique source generator", job.ID)
		}
		if err := projection.add(job); err != nil {
			return err
		}
		index := projection.groups[job.Groups[0]]
		projection.result.Expansions[index].Members = append(projection.result.Expansions[index].Members, job.ID)
	}
	projection.accepted[source.ID] = true
	return nil
}

func validateScheduleState(row NodeRow) error {
	if row.Generation < 0 {
		return fmt.Errorf("negative generation")
	}
	if row.Status != StatusDone && row.Outcome != "" {
		return fmt.Errorf("nonterminal row has an outcome")
	}
	if (row.ClaimedBy == "") != (row.LeaseExpiresAt == nil) || row.ClaimedBy == "" && row.CapabilityPrefix != "" {
		return fmt.Errorf("claim columns disagree")
	}
	switch row.Status {
	case StatusPending:
	case StatusRunning:
		if row.ClaimedBy == "" {
			return fmt.Errorf("running row has no holder")
		}
	case StatusApprovalPending:
		if row.ClaimedBy != "" {
			return fmt.Errorf("approval row has a holder")
		}
	case StatusDone:
		if row.ClaimedBy != "" {
			return fmt.Errorf("terminal row has a holder")
		}
		switch row.Outcome {
		case NodeSuccess, NodeFailed, NodeCancelled, NodeSkipped, NodeSuperseded, NodeMemoized:
		default:
			return fmt.Errorf("terminal row has an invalid outcome")
		}
	default:
		return fmt.Errorf("invalid status %q", row.Status)
	}
	return nil
}

func (projection *scheduleProjection) validate() error {
	if len(projection.rows) != len(projection.jobs) {
		return fmt.Errorf("persisted jobs include undeclared topology")
	}
	for id, job := range projection.jobs {
		row := projection.rows[id]
		valid := false
		switch row.Expansion {
		case ExpansionAbsent:
			valid = len(job.Closures.Generate) == 0
		case ExpansionExpected:
			valid = len(job.Closures.Generate) != 0 && !projection.result.CancelRequested && (row.Status != StatusDone || scheduleSucceeded(string(row.Outcome)))
		case ExpansionAccepted:
			valid = projection.accepted[id] && row.Status == StatusDone && scheduleSucceeded(string(row.Outcome))
		case ExpansionFailed:
			valid = len(job.Closures.Generate) != 0 && row.Status == StatusDone && scheduleSucceeded(string(row.Outcome))
		case ExpansionNone:
			valid = len(job.Closures.Generate) != 0 && (projection.result.CancelRequested || row.Status == StatusDone && !scheduleSucceeded(string(row.Outcome)))
		}
		if !valid {
			return fmt.Errorf("job %q has inconsistent expansion state %q", id, row.Expansion)
		}
	}
	return validateScheduleGraph(projection.result)
}

func validateScheduleGraph(snapshot ScheduleSnapshot) error {
	byID := make(map[string]int, len(snapshot.Nodes))
	groups := make(map[string]ScheduleExpansion, len(snapshot.Expansions))
	for index, node := range snapshot.Nodes {
		byID[node.ID] = index
	}
	for _, group := range snapshot.Expansions {
		groups[group.GeneratorID] = group
	}
	remaining := make([]int, len(snapshot.Nodes))
	waiters := make(map[string][]int)
	var queue []int
	for index, node := range snapshot.Nodes {
		deps, valid := scheduleDependencies(node, byID, groups)
		if !valid {
			return fmt.Errorf("job %q has undeclared dependencies", node.ID)
		}
		remaining[index] = len(deps)
		for _, id := range deps {
			waiters[id] = append(waiters[id], index)
		}
		if len(deps) == 0 {
			queue = append(queue, index)
		}
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		for _, index := range waiters[snapshot.Nodes[queue[cursor]].ID] {
			remaining[index]--
			if remaining[index] == 0 {
				queue = append(queue, index)
			}
		}
	}
	if len(queue) != len(snapshot.Nodes) {
		return fmt.Errorf("accepted scheduling graph contains a cycle")
	}
	return nil
}
