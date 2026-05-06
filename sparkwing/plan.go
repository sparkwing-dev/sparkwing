package sparkwing

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"
)

// Plan is the typed DAG a pipeline returns from its Plan method.
// The orchestrator consumes this, snapshots it, and dispatches nodes
// in dependency order. Build plans via NewPlan and the Sequence /
// Parallel combinators.
type Plan struct {
	mu         sync.Mutex
	nodes      []*Node
	byID       map[string]*Node
	expansions []Expansion
	// groups tracks every *NodeGroup declared via sw.Group in
	// declaration order. Unnamed groups are tracked too but contribute
	// no name to membership output.
	groups []*NodeGroup

	cache CacheOptions

	// lintWarnings accumulates non-fatal Plan-time advisories.
	lintWarnings []LintWarning
}

// LintWarning is a non-fatal Plan-time advisory attached to a node.
type LintWarning struct {
	NodeID string
	Code   string // short stable identifier, e.g. "node-stale-cache"
	Msg    string
}

// NewPlan returns an empty Plan.
func NewPlan() *Plan {
	return &Plan{byID: map[string]*Node{}}
}

// Nodes returns the plan's nodes in insertion order. For plans with
// dynamic expansion, returned slice reflects the current state; new
// nodes appear once their generator fires.
func (p *Plan) Nodes() []*Node {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*Node, len(p.nodes))
	copy(out, p.nodes)
	return out
}

// Node returns the node with the given ID, or nil if absent.
func (p *Plan) Node(id string) *Node {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byID[id]
}

// Expansions returns the registered ExpandFrom generators. Used by
// the orchestrator to drive dynamic fan-out.
func (p *Plan) Expansions() []Expansion {
	out := make([]Expansion, len(p.expansions))
	copy(out, p.expansions)
	return out
}

// ExpandGenerator is the closure signature for ExpandFrom. It runs
// once after the source node completes, with typed upstream output
// accessible via closure-captured Ref.Get(ctx). Return the list of
// children to materialize; each will automatically depend on the
// source node.
type ExpandGenerator func(ctx context.Context) []*Node

// Expansion ties a source node to its generator and resulting group.
type Expansion struct {
	Source *Node
	Group  *NodeGroup
	Gen    ExpandGenerator
}

// newNode builds a detached *Node from (id, job). Shared by Job
// (which then registers on the plan), JobFanOutDynamic children,
// Node.OnFailure recovery nodes, and the orchestrator's SpawnNode
// dispatch path -- every caller that needs a node before it has a
// home in p.nodes / p.byID.
//
// Runs the same validation Job does so a Produces/SetResult typo or
// an invalid Approval timeout panics at the same point regardless of
// where the node is destined to live.
//
// caller is the verb the user typed (e.g. "Job", "OnFailure"); it
// shows up in panic messages so the error points at the user's call
// site, not this helper.
func newNode(caller, id string, job Workable) *Node {
	if id == "" {
		panic(fmt.Sprintf("sparkwing: %s: id must not be empty", caller))
	}
	if job == nil {
		panic(fmt.Sprintf("sparkwing: %s(%q): job must be non-nil", caller, id))
	}

	// Approval gates: the *approvalJob Workable is a marker -- Work()
	// is empty and never executes; the orchestrator routes via
	// n.approval. Pipeline authors construct via sw.Approval, never
	// build *approvalJob directly.
	if app, ok := job.(*approvalJob); ok {
		switch app.cfg.OnExpiry {
		case "", ApprovalFail, ApprovalDeny, ApprovalApprove:
			// ok
		default:
			panic(fmt.Sprintf(
				"sparkwing: %s(%q): ApprovalConfig.OnExpiry = %q is not one of "+
					"sparkwing.ApprovalFail / ApprovalDeny / ApprovalApprove",
				caller, id, app.cfg.OnExpiry))
		}
		cfg := app.cfg
		return &Node{
			id:       id,
			job:      job,
			approval: &cfg,
		}
	}

	w := materializeWork(id, job)
	workType := outputTypeFromWork(w)

	// Strict Produces / SetResult contract: typed jobs must declare
	// both. Either alone is a Plan-time error.
	var outType reflect.Type
	pr, hasMarker := job.(producer)
	switch {
	case hasMarker && workType == nil:
		panic(fmt.Sprintf(
			"sparkwing: node %q: declares Produces[%v] but Work() never calls SetResult "+
				"(add a Result/Out step + w.SetResult, or drop the marker)",
			id, pr.producedType()))
	case !hasMarker && workType != nil:
		panic(fmt.Sprintf(
			"sparkwing: node %q: Work.SetResult declares output type %v but the job struct does not embed "+
				"sparkwing.Produces[%v] (add the marker so the contract is visible at the type level)",
			id, workType, workType))
	case hasMarker && workType != nil:
		declared := pr.producedType()
		if declared != workType {
			panic(fmt.Sprintf(
				"sparkwing: node %q: Produces[%v] but Work.SetResult is %v (align them)",
				id, declared, workType))
		}
		outType = declared
	}

	return &Node{
		id:      id,
		job:     job,
		work:    w,
		outType: outType,
	}
}

// NewDetachedNode builds a node with full Job-equivalent validation
// but does not register it on a Plan. Pipeline authors should not
// reach for this -- it exists for the orchestrator's SpawnNode
// dispatch path, where the child node is created at runtime and
// spliced in via Plan.InsertChild after the parent suspends. Use
// sparkwing.Job from pipeline code.
func NewDetachedNode(id string, job Workable) *Node {
	return newNode("NewDetachedNode", id, job)
}

// InsertChild splices a fresh node into the running plan WITHOUT
// wiring any dependency. Used by the SpawnNode dispatch path: the
// spawning runner is suspended waiting on the child's outcome, so
// adding child.Needs(parent) would deadlock.
func (p *Plan) InsertChild(child *Node) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if child == nil {
		return fmt.Errorf("InsertChild: nil child node")
	}
	if _, exists := p.byID[child.id]; exists {
		return fmt.Errorf("InsertChild: duplicate id %q", child.id)
	}
	p.byID[child.id] = child
	p.nodes = append(p.nodes, child)
	return nil
}

// InsertExpanded splices dynamically generated children into the plan.
// Each child automatically gets Needs(source). Called by the
// orchestrator.
func (p *Plan) InsertExpanded(source *Node, children []*Node) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, child := range children {
		if child == nil {
			return fmt.Errorf("ExpandFrom(%s): nil child node", source.id)
		}
		if _, exists := p.byID[child.id]; exists {
			return fmt.Errorf("ExpandFrom(%s): duplicate id %q", source.id, child.id)
		}
		child.addNeed(source.id)
		p.byID[child.id] = child
		p.nodes = append(p.nodes, child)
	}
	return nil
}

// Job registers a typed sparkwing.Workable under id and returns the node
// handle for further configuration (Needs, Env, etc.). The value must
// implement Work() *Work; the inner DAG is materialized at registration
// time so pipeline-explain / dashboard renderers / cycle detection see
// the full graph before dispatch starts.
//
// Approval gates are registered through the same verb by passing
// *Approval as the job. The orchestrator detects it and routes the
// node through the approval-waiter flow rather than executing Work().
//
// Output contract: a job that produces a typed value must embed
// sparkwing.Produces[T] AND call Work.SetResult on a step that returns
// T. Either marker or SetResult on its own is a Plan-time panic.
//
// For secrets, jobs call sparkwing.Secret(ctx, name) inside their step
// closures.
//
// Panics if id is empty, already registered, or job is nil.
//
//	sw.Job(plan, "test", sparkwing.JobFn(func(ctx context.Context) error {
//	    _, err := sparkwing.Bash(ctx, "go test ./...").Run()
//	    return err
//	}))
//
//	sw.Approval(plan, "approve-prod", sparkwing.ApprovalConfig{
//	    Message: "Promote build to prod?",
//	    Timeout: 2 * time.Hour,
//	}).Needs(integStg)
func Job(p *Plan, id string, job Workable) *Node {
	if p == nil {
		panic("sparkwing: Job: plan must be non-nil")
	}
	if _, ok := p.byID[id]; ok {
		panic(fmt.Sprintf("sparkwing: Job: duplicate node id %q", id))
	}
	n := newNode("Job", id, job)
	p.nodes = append(p.nodes, n)
	p.byID[id] = n
	return n
}

// LintWarnings returns the non-fatal Plan-time advisories accumulated
// while building this Plan. Surfaced by the orchestrator at dispatch
// and by `sparkwing pipeline explain --all`.
func (p *Plan) LintWarnings() []LintWarning {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]LintWarning, len(p.lintWarnings))
	copy(out, p.lintWarnings)
	return out
}

// materializeWork calls job.Work() under panic recovery so a
// pathological body produces a helpful Plan-time error.
func materializeWork(id string, job Workable) *Work {
	defer func() {
		if r := recover(); r != nil {
			panic(fmt.Sprintf("sparkwing: Plan-time materialization failed for node %q: %v", id, r))
		}
	}()
	w := job.Work()
	if w == nil {
		panic(fmt.Sprintf("sparkwing: Plan-time materialization failed for node %q: Job.Work() returned nil", id))
	}
	return w
}

func outputTypeFromWork(w *Work) reflect.Type {
	if w == nil {
		return nil
	}
	return w.ResultType()
}

// Node is a single entry in the Plan. It wraps a user-authored job
// plus dispatch modifiers.
type Node struct {
	id      string
	job     Workable
	work    *Work // materialized inner DAG; empty for Approval gates
	needs   []string
	env     map[string]string
	outType reflect.Type

	// Resilience modifiers. retryAuto switches from in-runner step
	// re-run to whole-node re-dispatch (right for infra flakes where
	// a fresh runner boot is more likely to recover).
	retryAttempts int
	retryBackoff  time.Duration
	retryAuto     bool
	timeout       time.Duration // per-attempt; zero = unlimited

	onFailure *Node // dispatched when this node fails

	// Multiple skipIf predicates accumulate with OR semantics.
	skipIf        []SkipPredicate
	skipIfTimeout time.Duration

	cache CacheOptions

	beforeRun []BeforeRunFn
	afterRun  []AfterRunFn

	// runsOn restricts the node to runners advertising all listed
	// labels. Empty = any runner may claim.
	runsOn []string

	// inline marks lightweight nodes for in-process execution on the
	// dispatcher, bypassing the configured Runner. Opt-in via
	// .Inline(); a CPU-bound or blocking inline node stalls the
	// dispatcher.
	inline bool

	// dynamic marks a node whose downstream shape isn't fully
	// predictable from the plan alone. Auto-inferred for ExpandFrom
	// sources; explicit via .Dynamic() otherwise.
	dynamic bool

	continueOnError bool
	optional        bool
	needsOptional   []string

	// Dynamic-group dependencies resolve at dispatch time rather than
	// plan construction.
	needsGroups []*NodeGroup

	// approval is non-nil when the node's job is an approval gate; the
	// orchestrator routes these through the approval waiter.
	approval *ApprovalConfig
}

// ApprovalConfig describes a manual approval gate. Authors fill it
// out and pass it to sw.Approval; the orchestrator reads it back via
// Node.ApprovalConfig when routing the gate to the approval-waiter
// flow.
//
//	approve := sw.Approval(plan, "approve-prod", sparkwing.ApprovalConfig{
//	    Message:  fmt.Sprintf("Promote %s to prod?", git.SHA),
//	    Timeout:  2 * time.Hour,
//	    OnExpiry: sparkwing.ApprovalFail,
//	}).Needs(integStg)
//	sw.Job(plan, "deploy-prod", &DeployJob{Env: "prod"}).Needs(approve)
type ApprovalConfig struct {
	// Message is the operator-facing prompt shown in the dashboard /
	// CLI. Empty falls back to a generic "Approve <node>?" in the UI.
	Message string
	// Timeout bounds how long the gate waits for a human answer. Zero
	// means never time out.
	Timeout time.Duration
	// OnExpiry controls how an unanswered gate resolves once Timeout
	// elapses. The zero value is ApprovalFail ("something went wrong,
	// the gate wasn't answered"). ApprovalDeny treats no-answer as a
	// soft "no" and ApprovalApprove as a soft "yes". Named OnExpiry
	// rather than OnTimeout to avoid confusion with Node.Timeout(),
	// which is unrelated (per-attempt execution budget).
	OnExpiry ApprovalTimeoutPolicy
}

// approvalJob is the unexported Workable that backs an approval gate.
// Pipeline authors don't construct it directly; sw.Approval builds it
// internally and the orchestrator detects it via the type assertion
// in newNode.
type approvalJob struct {
	Base
	cfg ApprovalConfig
}

// Work satisfies the Workable interface but never executes; approval
// nodes are routed through the approval-waiter path.
func (a *approvalJob) Work() *Work { return NewWork() }

// ApprovalTimeoutPolicy enumerates the resolution applied to an
// unanswered approval gate when its Timeout elapses.
type ApprovalTimeoutPolicy string

const (
	ApprovalFail    ApprovalTimeoutPolicy = "fail"
	ApprovalDeny    ApprovalTimeoutPolicy = "deny"
	ApprovalApprove ApprovalTimeoutPolicy = "approve"
)

// IsApproval reports whether the node is an approval gate.
func (n *Node) IsApproval() bool { return n.approval != nil }

// ApprovalConfig returns the per-node approval configuration, or nil
// for non-approval nodes. Used by the orchestrator to route gate
// nodes through the approval waiter; pipeline authors don't usually
// call this.
func (n *Node) ApprovalConfig() *ApprovalConfig { return n.approval }

// ApprovalGate is the handle returned by sw.Approval. It exposes a
// narrower modifier surface than *Node -- only the modifiers that
// make sense for a human gate (Needs, NeedsOptional, OnFailure
// recovery, BeforeRun/AfterRun hooks, SkipIf, Optional,
// ContinueOnError). Modifiers that don't apply to gates -- Retry,
// Timeout, Cache, RunsOn, Inline, Dynamic -- are physically absent
// from this type, so authoring those mistakes is a compile error
// rather than the previous mix of panics and silent no-ops.
type ApprovalGate struct {
	n *Node
}

// Approval registers a manual approval gate under id and returns the
// gate handle for further configuration. The orchestrator routes
// approval nodes through the approval-waiter flow rather than
// dispatching Work; the gate pauses until a human (via the dashboard
// or `sparkwing approve/deny`) resolves it. Denied / expired gates
// resolve per cfg.OnExpiry.
//
// Panics if id is empty or already registered, or if cfg.OnExpiry is
// not one of the documented constants.
//
//	approve := sw.Approval(plan, "approve-prod", sparkwing.ApprovalConfig{
//	    Message:  "Promote to prod?",
//	    Timeout:  2 * time.Hour,
//	    OnExpiry: sparkwing.ApprovalFail,
//	}).Needs(integStg)
//	sw.Job(plan, "deploy-prod", &DeployJob{}).Needs(approve)
func Approval(p *Plan, id string, cfg ApprovalConfig) *ApprovalGate {
	if p == nil {
		panic("sparkwing: Approval: plan must be non-nil")
	}
	if _, ok := p.byID[id]; ok {
		panic(fmt.Sprintf("sparkwing: Approval: duplicate node id %q", id))
	}
	n := newNode("Approval", id, &approvalJob{cfg: cfg})
	p.nodes = append(p.nodes, n)
	p.byID[id] = n
	return &ApprovalGate{n: n}
}

// Node returns the underlying *Node. Pipeline authors should rarely
// need this -- the gate's own methods cover the expected modifier
// surface; this is the escape hatch for orchestrator-internal code
// (or for the "I really want a Node-level modifier" case).
func (g *ApprovalGate) Node() *Node { return g.n }

// ID returns the gate's node id.
func (g *ApprovalGate) ID() string { return g.n.id }

// Needs declares hard upstream dependencies on the gate. Accepts the
// same shapes as Node.Needs (*Node, *NodeGroup, *ApprovalGate, string
// IDs, []*Node).
func (g *ApprovalGate) Needs(deps ...any) *ApprovalGate {
	g.n.Needs(deps...)
	return g
}

// NeedsOptional declares soft upstream dependencies; missing IDs are
// silently dropped instead of failing the run.
func (g *ApprovalGate) NeedsOptional(deps ...any) *ApprovalGate {
	g.n.NeedsOptional(deps...)
	return g
}

// OnFailure registers a recovery node that runs if the gate resolves
// to ApprovalFail. Useful for rollback / alert hooks.
func (g *ApprovalGate) OnFailure(id string, job Workable) *ApprovalGate {
	g.n.OnFailure(id, job)
	return g
}

// BeforeRun registers a hook that runs before the gate is presented
// to the operator. Multiple hooks accumulate.
func (g *ApprovalGate) BeforeRun(fn BeforeRunFn) *ApprovalGate {
	g.n.BeforeRun(fn)
	return g
}

// AfterRun registers a hook that runs after the gate resolves
// (regardless of outcome). Multiple hooks accumulate.
func (g *ApprovalGate) AfterRun(fn AfterRunFn) *ApprovalGate {
	g.n.AfterRun(fn)
	return g
}

// SkipIf registers a predicate that skips the gate (treats it as
// resolved Approve) when fn returns true. Multiple predicates OR
// together.
func (g *ApprovalGate) SkipIf(fn SkipPredicate, opts ...SkipOption) *ApprovalGate {
	g.n.SkipIf(fn, opts...)
	return g
}

// Optional marks the gate as optional: a non-Approve resolution
// doesn't fail downstream nodes whose only dep is this gate.
func (g *ApprovalGate) Optional() *ApprovalGate {
	g.n.Optional()
	return g
}

// ContinueOnError marks the gate so a failed resolution is treated
// as a soft failure for downstream propagation.
func (g *ApprovalGate) ContinueOnError() *ApprovalGate {
	g.n.ContinueOnError()
	return g
}

// SkipPredicate is a function evaluated by the orchestrator after
// upstream dependencies complete. Any registered predicate returning
// true marks the node Skipped without dispatching its job.
//
// Predicates MUST be cheap and side-effect-free; they run on the
// coordinator path with a short default timeout. Long-running logic
// belongs in a dedicated job feeding typed output via Ref.
type SkipPredicate func(ctx context.Context) bool

// CacheKey is the content-addressed identifier for a node's work.
// When two nodes produce the same CacheKey, the orchestrator can
// substitute the first completion's output for the second without
// re-running. Compose via sparkwing.Key(parts...) for deterministic
// hashing.
type CacheKey string

// CacheKeyFn computes a cache key after upstream dependencies
// complete. May read typed upstream output via Ref.Get(ctx). Return
// "" to opt out of caching for this invocation.
type CacheKeyFn func(ctx context.Context) CacheKey

// BeforeRunFn runs once before the first Run attempt. A non-nil error
// fails the node immediately; Run does not execute and Retry does
// not apply.
type BeforeRunFn func(ctx context.Context) error

// AfterRunFn runs once after Run (including all retries) terminates.
// err is the final Run error, or nil on success. The hook's return
// value is logged but does not change the node's outcome.
type AfterRunFn func(ctx context.Context, err error)

// ID returns the node's identifier.
func (n *Node) ID() string { return n.id }

// Job returns the underlying user-authored job struct.
func (n *Node) Job() Workable { return n.job }

// Work returns the materialized inner DAG for the node's job. Empty
// for Approval gates (their Work is never executed).
func (n *Node) Work() *Work { return n.work }

// Needs declares hard upstream dependencies. The orchestrator will not
// dispatch this node until every named dependency is satisfied.
//
// Accepts *Node, *NodeGroup, []*Node, or string IDs.
func (n *Node) Needs(deps ...any) *Node {
	for _, d := range deps {
		switch v := d.(type) {
		case *Node:
			if v != nil {
				n.addNeed(v.id)
			}
		case *ApprovalGate:
			if v != nil && v.n != nil {
				n.addNeed(v.n.id)
			}
		case *NodeGroup:
			if v != nil {
				if v.dynamic {
					// Resolve membership at dispatch, after the
					// expansion generator runs.
					n.needsGroups = append(n.needsGroups, v)
				} else {
					for _, m := range v.members {
						n.addNeed(m.id)
					}
				}
			}
		case string:
			if v != "" {
				n.addNeed(v)
			}
		case []*Node:
			for _, vv := range v {
				if vv != nil {
					n.addNeed(vv.id)
				}
			}
		default:
			panic(fmt.Sprintf("sparkwing: Node.Needs: unsupported dep type %T", d))
		}
	}
	return n
}

// NeedsGroups returns any dynamic groups (from ExpandFrom) this node
// is waiting on.
func (n *Node) NeedsGroups() []*NodeGroup { return n.needsGroups }

func (n *Node) addNeed(id string) {
	if slices.Contains(n.needs, id) {
		return
	}
	n.needs = append(n.needs, id)
}

// DepIDs returns the node IDs this node depends on. Includes both
// explicit Needs entries and Ref-derived edges (populated at plan
// finalization by the orchestrator).
func (n *Node) DepIDs() []string {
	out := make([]string, len(n.needs))
	copy(out, n.needs)
	return out
}

// Env sets a per-node environment variable. Overrides any inherited value.
func (n *Node) Env(key, value string) *Node {
	if n.env == nil {
		n.env = map[string]string{}
	}
	n.env[key] = value
	return n
}

// EnvMap returns the node's declared environment. Callers must not mutate.
func (n *Node) EnvMap() map[string]string { return n.env }

// OutputType returns the concrete Go type of the job's Run output, or
// nil if the job's Run returns no value beyond error.
func (n *Node) OutputType() reflect.Type { return n.outType }

// RetryConfig is the resolved retry envelope for a Node. Attempts ==
// 0 means no retry. Auto=true switches in-runner step re-run to
// whole-node re-dispatch.
type RetryConfig struct {
	Attempts int
	Backoff  time.Duration
	Auto     bool
}

// RetryOption tunes a Retry(...) call.
type RetryOption func(*RetryConfig)

// RetryBackoff sets the initial backoff between retry attempts. The
// orchestrator scales this exponentially per attempt. Zero = no
// delay.
func RetryBackoff(d time.Duration) RetryOption {
	return func(c *RetryConfig) {
		if d < 0 {
			d = 0
		}
		c.Backoff = d
	}
}

// RetryAuto switches the retry mechanism from in-runner step re-run
// to whole-node re-dispatch. Right for infra-level flakes (spot
// preemption, transient network errors, OOM kills).
func RetryAuto() RetryOption {
	return func(c *RetryConfig) { c.Auto = true }
}

// Retry configures the node to be re-attempted up to attempts
// additional times on failure. Zero (the default) means no retry.
// Cancelled nodes are never retried.
//
//	sw.Job(plan, "flaky-test", &TestJob{}).Retry(2)
//	sw.Job(plan, "flaky-test", &TestJob{}).Retry(3, sw.RetryBackoff(500*time.Millisecond))
//	sw.Job(plan, "push", &PushJob{}).Retry(2, sw.RetryAuto())
//	sw.Job(plan, "push", &PushJob{}).Retry(2, sw.RetryBackoff(5*time.Second), sw.RetryAuto())
func (n *Node) Retry(attempts int, opts ...RetryOption) *Node {
	if attempts < 0 {
		attempts = 0
	}
	cfg := RetryConfig{Attempts: attempts}
	for _, opt := range opts {
		opt(&cfg)
	}
	n.retryAttempts = cfg.Attempts
	n.retryBackoff = cfg.Backoff
	n.retryAuto = cfg.Auto
	return n
}

// Timeout caps the per-attempt duration. Exceeding the timeout
// cancels the job's context, returns an ExecError, and is treated
// as a normal failure (eligible for retries).
func (n *Node) Timeout(d time.Duration) *Node {
	n.timeout = d
	return n
}

// OnFailure registers a recovery node that runs only when this node
// terminates with outcome=failed; otherwise it's marked Skipped. The
// recovery inherits no dependencies from its parent. Useful for
// rollback, alerting, and cleanup hooks.
func (n *Node) OnFailure(id string, job Workable) *Node {
	n.onFailure = newNode("OnFailure", id, job)
	return n
}

// RetryConfig returns the resolved retry envelope. A zero-value
// RetryConfig means no retry.
func (n *Node) RetryConfig() RetryConfig {
	return RetryConfig{
		Attempts: n.retryAttempts,
		Backoff:  n.retryBackoff,
		Auto:     n.retryAuto,
	}
}

// TimeoutDuration returns the configured per-attempt timeout, or zero
// if unlimited.
func (n *Node) TimeoutDuration() time.Duration { return n.timeout }

// OnFailureNode returns the recovery node registered via OnFailure, or
// nil if none.
func (n *Node) OnFailureNode() *Node { return n.onFailure }

// OnFailureNodeID returns the ID of the recovery node registered via
// OnFailure, or "" if none. Mirrors OnFailureNode() but returns just
// the identifier so plan-introspection callers (PreviewPlan, the
// orchestrator's snapshot encoder, dashboard renderers) can surface
// the failure-branch attachment without dereferencing the unexported
// onFailure field. IMP-029.
func (n *Node) OnFailureNodeID() string {
	if n.onFailure == nil {
		return ""
	}
	return n.onFailure.ID()
}

// SkipOption configures a SkipIf registration.
type SkipOption func(*Node)

// SkipBudget overrides the per-predicate evaluation budget. Zero
// uses the orchestrator's default. The budget is per-node, not
// per-predicate: the last SkipBudget on a node wins.
func SkipBudget(d time.Duration) SkipOption {
	return func(n *Node) {
		if d < 0 {
			d = 0
		}
		n.skipIfTimeout = d
	}
}

// SkipIf registers a predicate the orchestrator evaluates after this
// node's dependencies complete. If the predicate returns true, the
// node is marked Skipped with the reason and its work is never
// dispatched.
//
// Typed upstream output is consumed via closure capture + Ref.Get:
//
//	setup := sw.Job(plan, "setup", &SetupJob{})
//	setupOut := sw.RefTo[SetupOutput](setup)
//	sw.Job(plan, "deploy", &DeployJob{}).
//	    Needs(setup).
//	    SkipIf(func(ctx context.Context) bool {
//	        return setupOut.Get(ctx).SkipDeploy
//	    })
//
// Multiple SkipIf calls accumulate with OR semantics: any true result
// skips the node. Predicates run on the coordinator path with a
// default 30s budget; pass SkipBudget(d) to override:
//
//	deploy.SkipIf(pred, sparkwing.SkipBudget(2*time.Minute))
func (n *Node) SkipIf(fn SkipPredicate, opts ...SkipOption) *Node {
	if fn != nil {
		n.skipIf = append(n.skipIf, fn)
	}
	for _, opt := range opts {
		if opt != nil {
			opt(n)
		}
	}
	return n
}

// SkipPredicates returns the node's registered skip predicates.
func (n *Node) SkipPredicates() []SkipPredicate { return n.skipIf }

// SkipIfBudget returns the configured per-predicate evaluation budget,
// or zero for the orchestrator's default.
func (n *Node) SkipIfBudget() time.Duration { return n.skipIfTimeout }

// BeforeRun registers a hook to run once before the node's Run method
// on the first attempt. A non-nil error fails the node without
// invoking Run and without retrying.
func (n *Node) BeforeRun(fn BeforeRunFn) *Node {
	if fn != nil {
		n.beforeRun = append(n.beforeRun, fn)
	}
	return n
}

// AfterRun registers a hook to run once after Run terminates,
// including after all retries. The hook receives the final error
// (nil on success); its own failure is logged but does not change
// the node's outcome.
func (n *Node) AfterRun(fn AfterRunFn) *Node {
	if fn != nil {
		n.afterRun = append(n.afterRun, fn)
	}
	return n
}

// BeforeRunHooks returns the node's registered pre-run hooks.
func (n *Node) BeforeRunHooks() []BeforeRunFn { return n.beforeRun }

// AfterRunHooks returns the node's registered post-run hooks.
func (n *Node) AfterRunHooks() []AfterRunFn { return n.afterRun }

// RunsOn restricts this node to runners advertising every label in
// the given set. Semantics are AND, not OR: .RunsOn("arm64", "laptop")
// requires both labels; a runner advertising a superset still matches.
// To express OR, author separate nodes.
//
// Labels are equality strings; common conventions are bare tags
// ("laptop", "gpu") or key=value ("arch=arm64"). Calling RunsOn with
// no arguments clears any previously-set labels.
//
//	plan.Add("train", &TrainJob{}).RunsOn("gpu")
//	plan.Add("package-arm", &PackageJob{}).RunsOn("arch=arm64", "trusted")
func (n *Node) RunsOn(labels ...string) *Node {
	if len(labels) == 0 {
		n.runsOn = nil
		return n
	}
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	n.runsOn = out
	return n
}

// RunsOnLabels returns the labels declared via RunsOn.
func (n *Node) RunsOnLabels() []string {
	if len(n.runsOn) == 0 {
		return nil
	}
	out := make([]string, len(n.runsOn))
	copy(out, n.runsOn)
	return out
}

// Inline marks the node for in-process execution on the dispatcher,
// bypassing the configured Runner. Useful for lightweight glue work
// (setup checks, result aggregation) that would otherwise force a
// multi-second runner boot.
//
// Constraints:
//
//   - The job runs on the dispatcher's goroutine pool; a long or
//     CPU-heavy inline node delays other nodes. Keep inline jobs
//     under a second or two.
//
//   - Retry / Timeout / CacheKey still apply; only runner placement
//     changes.
//
//   - RunsOn labels are ignored for inline nodes (there's no runner
//     to match). Combining Inline() + RunsOn() is a config warning.
//
//     plan.Add("setup", &SetupJob{}).Inline()
//     plan.Add("summarize", &SummaryJob{}).Needs(testBuckets).Inline()
func (n *Node) Inline() *Node {
	if n.approval != nil {
		panic(fmt.Sprintf("sparkwing: Node.Inline: approval gate %q cannot be inlined; approvals are long-lived by design", n.id))
	}
	n.inline = true
	return n
}

// IsInline reports whether the node was marked for orchestrator-local
// execution via Inline(). The dispatcher uses this to route the node
// to the in-process runner regardless of the configured Runner.
func (n *Node) IsInline() bool { return n.inline }

// NodeGroupNames returns the names of every declared *NodeGroup whose
// members include the given node. Unnamed groups are skipped. Order
// is declaration order; duplicates are collapsed.
func (p *Plan) NodeGroupNames(id string) []string {
	if len(p.groups) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, g := range p.groups {
		if g == nil || g.Name() == "" {
			continue
		}
		for _, m := range g.Members() {
			if m != nil && m.id == id {
				if !seen[g.Name()] {
					seen[g.Name()] = true
					out = append(out, g.Name())
				}
				break
			}
		}
	}
	return out
}

// Dynamic marks the node as having runtime-variable downstream work
// (e.g. invokes RunAndAwait, enqueues external tasks). ExpandFrom
// sources are auto-detected; .Dynamic() is only needed for
// non-ExpandFrom cases. Dynamic is purely a signal to readers.
//
//	plan.Add("orchestrate", &OrchestrateJob{}).Dynamic()
func (n *Node) Dynamic() *Node {
	n.dynamic = true
	return n
}

// IsDynamic reports whether .Dynamic() was called. For the effective-
// dynamic status (which includes ExpandFrom sources), use
// Plan.IsDynamicNode(id).
func (n *Node) IsDynamic() bool { return n.dynamic }

// GroupSourceIDs returns the ids of the source nodes backing any
// ExpandFrom Groups this node waits on via Needs(group). Returns nil
// when the node has no dynamic-group deps.
func (p *Plan) GroupSourceIDs(id string) []string {
	n := p.Node(id)
	if n == nil || len(n.needsGroups) == 0 {
		return nil
	}
	want := make(map[*NodeGroup]bool, len(n.needsGroups))
	for _, g := range n.needsGroups {
		want[g] = true
	}
	out := make([]string, 0, len(n.needsGroups))
	for _, exp := range p.expansions {
		if exp.Group != nil && exp.Source != nil && want[exp.Group] {
			out = append(out, exp.Source.id)
		}
	}
	return out
}

// IsDynamicNode reports whether the node should render as dynamic:
// .Dynamic() was called or it's the source of an ExpandFrom expansion.
func (p *Plan) IsDynamicNode(id string) bool {
	n := p.Node(id)
	if n != nil && n.dynamic {
		return true
	}
	for _, exp := range p.expansions {
		if exp.Source != nil && exp.Source.id == id {
			return true
		}
	}
	return false
}

// ContinueOnError tells the orchestrator that downstream dependents
// should proceed even when this node fails. The run as a whole still
// reports as failed (use Optional to also suppress run-level failure).
func (n *Node) ContinueOnError() *Node {
	n.continueOnError = true
	return n
}

// IsContinueOnError reports whether downstream should ignore this
// node's failure for dispatch purposes.
func (n *Node) IsContinueOnError() bool { return n.continueOnError }

// Optional marks the node as non-essential: a failure is logged as a
// warning and does not count toward the run's overall success/fail
// outcome. Implies ContinueOnError.
func (n *Node) Optional() *Node {
	n.optional = true
	n.continueOnError = true
	return n
}

// IsOptional reports whether the node is marked non-essential.
func (n *Node) IsOptional() bool { return n.optional }

// NeedsOptional declares upstream dependencies that may or may not be
// present in the plan. Unknown IDs are silently dropped; known IDs
// behave like Needs. Accepts the same shapes as Needs.
func (n *Node) NeedsOptional(deps ...any) *Node {
	for _, d := range deps {
		switch v := d.(type) {
		case *Node:
			if v != nil {
				n.needsOptional = append(n.needsOptional, v.id)
			}
		case *ApprovalGate:
			if v != nil && v.n != nil {
				n.needsOptional = append(n.needsOptional, v.n.id)
			}
		case *NodeGroup:
			if v != nil {
				for _, m := range v.members {
					n.needsOptional = append(n.needsOptional, m.id)
				}
			}
		case string:
			if v != "" {
				n.needsOptional = append(n.needsOptional, v)
			}
		case []*Node:
			for _, vv := range v {
				if vv != nil {
					n.needsOptional = append(n.needsOptional, vv.id)
				}
			}
		}
	}
	return n
}

// OptionalDepIDs returns the IDs declared via NeedsOptional.
func (n *Node) OptionalDepIDs() []string {
	out := make([]string, len(n.needsOptional))
	copy(out, n.needsOptional)
	return out
}
