package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// PlanNodeID is the node every controller-dispatched run starts with. Its
// claimant plans the run and submits the result to [Store.AcceptPlan]; no
// submitted node may take the ID.
const PlanNodeID = "plan"

// Plan limits. A submitted plan above a cap is refused, and a resource or
// retry request above its ceiling is clamped to it.
const (
	MaxPlanBytes      = MaxNodeDispatchEnvelope
	MaxPlanNodes      = 1000
	MaxNodeIDBytes    = 200
	MaxNodeRetries    = 10
	MaxRetryBackoff   = 5 * time.Minute
	maxNodeLabels     = 32
	maxNodeLabelBytes = 128
)

// ErrPlanInvalid refuses a submitted plan the controller will not run; the
// wrapped message names the first reason.
var ErrPlanInvalid = errors.New("store: plan refused")

// ErrPlanAccepted refuses a plan-snapshot upload for a run whose planning
// claim's plan was accepted; that plan is the run's for good.
var ErrPlanAccepted = errors.New("store: the run's plan was accepted from its planning claim")

var specHashRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type submittedPlan struct {
	Requires  []string          `json:"requires"`
	Nodes     []submittedNode   `json:"nodes"`
	PlanConc  *json.RawMessage  `json:"plan_concurrency"`
	PlanConcs []json.RawMessage `json:"plan_concurrency_groups"`
}

type submittedNode struct {
	ID           string           `json:"id"`
	Deps         []string         `json:"deps"`
	OptionalDeps []string         `json:"optional_deps"`
	SpecHash     string           `json:"spec_hash"`
	Dynamic      bool             `json:"dynamic"`
	Approval     *json.RawMessage `json:"approval"`
	OnFailureOf  string           `json:"on_failure_of"`
	Modifiers    json.RawMessage  `json:"modifiers"`
}

// safety: decoded strictly, so a modifier this store does not know is refused
// rather than dropped; every field here is either enforced or runs in the pod.
type submittedModifiers struct {
	Retry               int      `json:"retry"`
	RetryBackoffMS      int64    `json:"retry_backoff_ms"`
	RetryAuto           bool     `json:"retry_auto"`
	TimeoutMS           int64    `json:"timeout_ms"`
	NoProgressTimeoutMS int64    `json:"no_progress_timeout_ms"`
	RunsOn              []string `json:"runs_on"`
	Prefers             []string `json:"prefers"`
	WhenRunner          []string `json:"when_runner"`
	Cache               bool     `json:"cache"`
	CacheTTLMS          int64    `json:"cache_ttl_ms"`
	ConcGroup           string   `json:"conc_group"`
	ConcCapacity        int      `json:"conc_capacity"`
	ConcCost            int      `json:"conc_cost"`
	ConcScope           string   `json:"conc_scope"`
	ConcOnLimit         string   `json:"conc_on_limit"`
	ConcQueueTimeoutMS  int64    `json:"conc_queue_timeout_ms"`
	ConcCancelTimeoutMS int64    `json:"conc_cancel_timeout_ms"`
	ResCores            float64  `json:"res_cores"`
	ResMemoryBytes      int64    `json:"res_memory_bytes"`
	Inline              bool     `json:"inline"`
	Optional            bool     `json:"optional"`
	ContinueOnError     bool     `json:"continue_on_error"`
	OnFailure           string   `json:"on_failure"`
	HasBeforeRun        bool     `json:"has_before_run"`
	HasAfterRun         bool     `json:"has_after_run"`
	HasSkipIf           bool     `json:"has_skip_if"`
}

type plannedNode struct {
	id, kind, specHash, onFailureOf string
	approval                        []byte
	deps, runsOn, prefers           []string
	resource                        ExecutorResource
	retryBudget                     int
	retryBackoff, timeout           time.Duration
	optional, continueOnError       bool
}

func planRefused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPlanInvalid, fmt.Sprintf(format, args...))
}

// safety: a planning claim lost with its machine is planned once more, as any
// node within its retry budget is; a second loss fails the run.
const planNodeRetryBudget = 1

// CreatePlanNode starts a controller-dispatched run: it adds the run's
// [PlanNodeID] node, ready for a planning claim. The run must exist in team
// and have no nodes.
func (s *Store) CreatePlanNode(ctx context.Context, team Team, runID string, now time.Time) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockDispatchRunTx(ctx, tx, team, runID); err != nil {
		return err
	}
	if err := insertPlanNodeTx(ctx, tx, team, runID, RepoDispatchController, now); err != nil {
		return err
	}
	return tx.Commit()
}

func insertPlanNodeTx(ctx context.Context, tx *storeTx, team Team, runID string, dispatch RepoDispatch, now time.Time) error {
	class, err := readyCPUClassTx(ctx, tx, ExecutorResource{})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO nodes (team, run_id, node_id, status, kind, deps_json,
       credit_cpu_class, ready_at, placement_hold_from, retry_budget, seq)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		string(team), runID, PlanNodeID, nodeStatusPending, nodeKindPlan, []byte("[]"),
		class, now.UnixNano(), now.UnixNano(), planNodeRetryBudget); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET admission = ?, dispatch = ? WHERE team = ? AND id = ?`,
		RunAdmissionPlanning, string(dispatch), string(team), runID)
	return err
}

// AcceptPlan commits the plan body a planning claim submits, as the claim's
// one result. In one transaction under the run row it validates the plan,
// inserts its nodes, finishes the plan node, records the accepted generation,
// settles the run and bills the planning claim. It reports replayed when the
// claim already committed this exact body, and writes nothing then.
//
// A refused plan returns an error wrapping [ErrPlanInvalid] and writes
// nothing, so the claimant reports the failure through [Store.ReportAttempt].
// A run accepts one plan: a claim at any other generation, or a different
// body from this one, is [ErrClaimResultConflict].
func (s *Store) AcceptPlan(ctx context.Context, commit ClaimResultCommit, body []byte, now time.Time) (replayed bool, err error) {
	tok := commit.Token()
	if tok.Kind != ClaimTokenPlan || tok.NodeID != PlanNodeID {
		return false, fmt.Errorf("%w: a plan is submitted by the %q node's plan claim", ErrClaimResultConflict, PlanNodeID)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := lockDispatchRunTx(ctx, tx, tok.Team, tok.RunID); err != nil {
		return false, err
	}
	if replayed, err := commit.commitTx(ctx, tx, now); err != nil || replayed {
		return replayed, err
	}
	var accepted int64
	if err := tx.QueryRowContext(ctx, `SELECT plan_accepted_generation FROM runs WHERE team = ? AND id = ?`,
		string(tok.Team), tok.RunID).Scan(&accepted); err != nil {
		return false, err
	}
	if accepted != 0 {
		return false, ErrClaimResultConflict
	}
	cancelled, err := claimRunCancelled(ctx, tx.QueryRowContext, tok.Team, tok.RunID)
	if err != nil {
		return false, err
	}
	if cancelled {
		return false, ErrClaimCancelRequested
	}
	planned, err := validatePlan(body)
	if err != nil {
		return false, err
	}
	table, err := creditRateTableTx(ctx, tx)
	if err != nil {
		return false, err
	}
	for i, n := range planned {
		if err := insertPlannedNodeTx(ctx, tx, tok.Team, tok.RunID, n, table, int64(i+1)); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET plan_json = ?, plan_accepted_generation = ?, admission = ?,
       status = CASE WHEN status = ? THEN ? ELSE status END
 WHERE team = ? AND id = ?`, body, tok.Generation, RunAdmissionAdmitted, runStatusPending, runStatusRunning,
		string(tok.Team), tok.RunID); err != nil {
		return false, err
	}
	if err := s.commitAttemptTx(ctx, tx, tok, AttemptReport{Outcome: outcomeSuccess}, now); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func insertPlannedNodeTx(ctx context.Context, tx *storeTx, team Team, runID string, n plannedNode,
	table CreditRateTable, seq int64,
) error {
	if err := enforceNodesPerRunTx(ctx, tx, team, runID, n.id); err != nil {
		var limit *ComputeLimitError
		if errors.As(err, &limit) {
			return planRefused("%v", err)
		}
		return err
	}
	res := n.resource
	var class int64
	if len(table) > 0 {
		largest := table[len(table)-1].Cores
		res.Cores = min(res.Cores, float64(largest))
		res.MemoryBytes = min(res.MemoryBytes, CPUClassMemoryBytes(largest))
		rate, err := table.ClassForResource(res)
		if err != nil {
			return err
		}
		class = rate.Cores
	}
	deps, err := json.Marshal(n.deps)
	if err != nil {
		return err
	}
	needs, err := labelsJSON(n.runsOn)
	if err != nil {
		return err
	}
	prefers, err := labelsJSON(n.prefers)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO nodes (team, run_id, node_id, status, kind, deps_json,
       needs_labels, prefers_labels, requested_cores, requested_memory_bytes, credit_cpu_class,
       spec_hash, on_failure_of, continue_on_error, optional, retry_budget, retry_backoff_ms, seq, approval_json, timeout_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(team), runID, n.id, nodeStatusPending, n.kind, deps,
		needs, prefers, res.Cores, res.MemoryBytes, class,
		n.specHash, n.onFailureOf, boolInt(n.continueOnError), boolInt(n.optional), n.retryBudget,
		n.retryBackoff.Milliseconds(), seq, n.approval, n.timeout.Milliseconds())
	return err
}

func labelsJSON(labels []string) ([]byte, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	return json.Marshal(labels)
}

func validatePlan(body []byte) ([]plannedNode, error) {
	if len(body) > MaxPlanBytes {
		return nil, planRefused("the plan is %d bytes; the limit is %d", len(body), MaxPlanBytes)
	}
	var plan submittedPlan
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&plan); err != nil {
		return nil, planRefused("the plan is not a JSON document: %v", err)
	}
	if dec.More() {
		return nil, planRefused("the plan holds more than one JSON document")
	}
	if plan.PlanConc != nil || len(plan.PlanConcs) > 0 {
		return nil, planRefused("plan-level concurrency groups are not supported; declare concurrency on nodes")
	}
	if len(plan.Nodes) > MaxPlanNodes {
		return nil, planRefused("the plan has %d nodes; the limit is %d", len(plan.Nodes), MaxPlanNodes)
	}
	known := make(map[string]bool, len(plan.Nodes))
	for _, n := range plan.Nodes {
		if err := validNodeID(n.ID); err != nil {
			return nil, planRefused("node %q: %v", n.ID, err)
		}
		if known[n.ID] {
			return nil, planRefused("node %q appears twice", n.ID)
		}
		known[n.ID] = true
	}
	out := make([]plannedNode, 0, len(plan.Nodes))
	for _, n := range plan.Nodes {
		p, err := planNode(n, known, plan.Requires)
		if err != nil {
			return nil, planRefused("node %q: %v", n.ID, err)
		}
		out = append(out, p)
	}
	if cycle := planCycle(out); cycle != "" {
		return nil, planRefused("the dependency graph has a cycle through %q", cycle)
	}
	return out, nil
}

// safety: the node ID lands in URL paths and file names, so this is the SDK's
// rule (no empty, "." or ".." segment, no backslash or control character) plus
// a length cap and valid UTF-8, which the SDK leaves to Go's string handling.
func validNodeID(id string) error {
	switch {
	case id == "":
		return errors.New("the ID is empty")
	case len(id) > MaxNodeIDBytes:
		return fmt.Errorf("the ID is longer than %d bytes", MaxNodeIDBytes)
	case !utf8.ValidString(id):
		return errors.New("the ID is not valid UTF-8")
	case id == PlanNodeID:
		return fmt.Errorf("the ID %q is reserved for the planning node", PlanNodeID)
	}
	for _, seg := range strings.Split(id, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("the ID has an empty or relative path segment")
		}
		for _, r := range seg {
			if r == '\\' || r < 0x20 || r == 0x7f {
				return errors.New("the ID holds a backslash or a control character")
			}
		}
	}
	return nil
}

func planNode(n submittedNode, known map[string]bool, requires []string) (plannedNode, error) {
	switch {
	case n.Dynamic:
		return plannedNode{}, errors.New("dynamic fan-out is not supported")
	case !specHashRe.MatchString(n.SpecHash):
		return plannedNode{}, errors.New(`spec_hash must be "sha256:" and 64 lowercase hex digits`)
	}
	p := plannedNode{id: n.ID, kind: nodeKindWork, specHash: n.SpecHash, onFailureOf: n.OnFailureOf}
	seen := map[string]bool{}
	for _, dep := range n.Deps {
		if !known[dep] {
			return plannedNode{}, fmt.Errorf("depends on %q, which the plan does not hold", dep)
		}
		if !seen[dep] {
			seen[dep] = true
			p.deps = append(p.deps, dep)
		}
	}
	// safety: the SDK drops an optional dependency the plan lacks and treats a
	// present one exactly like a hard one.
	for _, dep := range n.OptionalDeps {
		if known[dep] && !seen[dep] {
			seen[dep] = true
			p.deps = append(p.deps, dep)
		}
	}
	if seen[n.ID] || n.OnFailureOf == n.ID {
		return plannedNode{}, errors.New("depends on itself")
	}
	if n.OnFailureOf != "" && !known[n.OnFailureOf] {
		return plannedNode{}, fmt.Errorf("recovers %q, which the plan does not hold", n.OnFailureOf)
	}
	if p.deps == nil {
		p.deps = []string{}
	}
	var m submittedModifiers
	if len(n.Modifiers) > 0 && string(n.Modifiers) != "null" {
		dec := json.NewDecoder(bytes.NewReader(n.Modifiers))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			return plannedNode{}, fmt.Errorf("modifiers: %w", err)
		}
	}
	if err := checkModifiers(&m); err != nil {
		return plannedNode{}, err
	}
	if m.OnFailure != "" && !known[m.OnFailure] {
		return plannedNode{}, fmt.Errorf("names recovery node %q, which the plan does not hold", m.OnFailure)
	}
	// safety: the pipeline's requirements bind every node, as the in-process
	// dispatcher merges them into each node's claim labels.
	m.RunsOn = mergeLabels(m.RunsOn, requires)
	for _, labels := range [][]string{m.RunsOn, m.Prefers} {
		if len(labels) > maxNodeLabels {
			return plannedNode{}, fmt.Errorf("more than %d runner labels", maxNodeLabels)
		}
		for _, l := range labels {
			if l == "" || len(l) > maxNodeLabelBytes {
				return plannedNode{}, fmt.Errorf("runner label %q is empty or longer than %d bytes", l, maxNodeLabelBytes)
			}
		}
	}
	if n.Approval != nil {
		if m.HasSkipIf {
			return plannedNode{}, errors.New("an approval gate cannot carry SkipIf")
		}
		approval, err := approvalJSON(*n.Approval)
		if err != nil {
			return plannedNode{}, err
		}
		p.kind, p.approval, m.RetryAuto = nodeKindApproval, approval, false
	}
	p.runsOn, p.prefers = m.RunsOn, m.Prefers
	p.optional, p.continueOnError = m.Optional, m.ContinueOnError || m.Optional
	if m.RetryAuto {
		p.retryBudget = min(max(m.Retry, 0), MaxNodeRetries)
		p.retryBackoff = min(max(time.Duration(m.RetryBackoffMS)*time.Millisecond, 0), MaxRetryBackoff)
	}
	if !math.IsNaN(m.ResCores) && m.ResCores > 0 {
		p.resource.Cores = m.ResCores
	}
	p.resource.MemoryBytes = max(m.ResMemoryBytes, 0)
	p.timeout = min(time.Duration(m.TimeoutMS)*time.Millisecond, MaxClaimTokenLifetime)
	return p, nil
}

const maxApprovalMessageBytes = 4 << 10

func approvalJSON(raw json.RawMessage) ([]byte, error) {
	var spec approvalSpec
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("approval: %w", err)
	}
	switch spec.OnTimeout {
	case "":
		spec.OnTimeout = ApprovalOnTimeoutFail
	case ApprovalOnTimeoutFail, ApprovalOnTimeoutDeny, ApprovalOnTimeoutApprove:
	default:
		return nil, fmt.Errorf("approval on_timeout %q is not fail, deny or approve", spec.OnTimeout)
	}
	if spec.TimeoutMS < 0 {
		return nil, errors.New("approval timeout_ms is negative")
	}
	if len(spec.Message) > maxApprovalMessageBytes {
		return nil, fmt.Errorf("approval message is longer than %d bytes", maxApprovalMessageBytes)
	}
	return json.Marshal(spec)
}

func checkModifiers(m *submittedModifiers) error {
	switch {
	case len(m.WhenRunner) > 0:
		return errors.New("when_runner is not supported yet")
	case m.ConcScope == "box":
		return errors.New(`concurrency scope "box" bounds one machine, and each node runs on a machine of its own`)
	case m.ConcScope != "" && m.ConcScope != "run" && m.ConcScope != "global":
		return fmt.Errorf("concurrency scope %q is not run or global", m.ConcScope)
	case m.ConcOnLimit != "" && m.ConcOnLimit != "queue" && m.ConcOnLimit != "fail" &&
		m.ConcOnLimit != "skip" && m.ConcOnLimit != "cancel_others":
		return fmt.Errorf("concurrency on_limit %q is not queue, fail, skip or cancel_others", m.ConcOnLimit)
	case m.ConcGroup == "" && (m.ConcScope != "" || m.ConcOnLimit != "" || m.ConcCapacity != 0 || m.ConcCost != 0):
		return errors.New("concurrency settings name no conc_group")
	case m.ConcCapacity < 0 || m.ConcCost < 0 || m.TimeoutMS < 0 || m.NoProgressTimeoutMS < 0 ||
		m.CacheTTLMS < 0 || m.ConcQueueTimeoutMS < 0 || m.ConcCancelTimeoutMS < 0:
		return errors.New("a count or duration is negative")
	}
	return nil
}

func mergeLabels(own, extra []string) []string {
	out := make([]string, 0, len(own)+len(extra))
	seen := make(map[string]bool, len(own)+len(extra))
	for _, l := range append(slices.Clone(own), extra...) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func planCycle(nodes []plannedNode) string {
	indegree := make(map[string]int, len(nodes))
	dependents := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		upstream := n.deps
		if n.onFailureOf != "" {
			upstream = append(append([]string{}, n.deps...), n.onFailureOf)
		}
		indegree[n.id] += len(upstream)
		for _, u := range upstream {
			dependents[u] = append(dependents[u], n.id)
		}
	}
	queue := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if indegree[n.id] == 0 {
			queue = append(queue, n.id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, d := range dependents[id] {
			indegree[d]--
			if indegree[d] == 0 {
				queue = append(queue, d)
			}
		}
	}
	for _, n := range nodes {
		if indegree[n.id] > 0 {
			return n.id
		}
	}
	return ""
}
