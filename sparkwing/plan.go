package sparkwing

import (
	"context"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// Plan holds the job graph populated by a pipeline's Plan method.
// Build a Plan via [NewPlan], attach jobs with [Job], and chain
// dependencies with [JobNode.Needs].
type Plan struct {
	diagnostics               *planningDiagnostics
	evalGate                  chan struct{}
	generated                 map[string][]nodeproto.JobSpec
	secretInputs              []string
	registration              *registration
	source                    *Checkout
	prioritySet, claimWaitSet bool
	mu                        sync.Mutex
	nodes                     []*JobNode
	byID                      map[string]*JobNode

	groups []*JobGroup

	planConcurrency []planConcurrency

	resources *ResourceHints

	priority int

	claimWait time.Duration

	admissionClass AdmissionClass

	inputs any
	args   map[string]string

	jobArgs []jobArgsDecl
}

func NewPlan() *Plan {
	return &Plan{byID: map[string]*JobNode{}, evalGate: make(chan struct{}, 1)}
}

func recoveryChain(root *JobNode) iter.Seq[*JobNode] {
	return func(yield func(*JobNode) bool) {
		for n := root; n != nil; n = n.onFailure {
			if !yield(n) {
				return
			}
		}
	}
}

func (p *Plan) setInputs(in any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inputs = in
}

func validateNodeID(id string) error {
	for _, seg := range strings.Split(id, "/") {
		if seg == "" {
			return fmt.Errorf("id must not contain empty path segments")
		}
		if seg == "." || seg == ".." {
			return fmt.Errorf("id segment %q is a path reference", seg)
		}
		for _, r := range seg {
			if r == '\\' || r < 0x20 || r == 0x7f {
				return fmt.Errorf("id must not contain backslashes or control characters")
			}
		}
	}
	return nil
}

func newNode(caller, id string, job Workable) *JobNode {
	if id == "" {
		panic(fmt.Sprintf("sparkwing: %s: id must not be empty", caller))
	}
	if err := validateNodeID(id); err != nil {
		panic(fmt.Sprintf("sparkwing: %s(%q): %v", caller, id, err))
	}
	if job == nil {
		panic(fmt.Sprintf("sparkwing: %s(%q): job must be non-nil", caller, id))
	}

	w, resultStep := materializeWork(id, job)
	var workType reflect.Type
	if resultStep != nil {
		workType = resultStep.outType
	}

	var outType reflect.Type
	pr, hasMarker := job.(producer)
	switch {
	case hasMarker && workType == nil:
		panic(fmt.Sprintf(
			"sparkwing: node %q: declares Produces[%v] but Work() returned no typed step "+
				"(return a typed *WorkStep matching Produces[%v], or drop the marker)",
			id, pr.producedType(), pr.producedType(),
		))
	case !hasMarker && workType != nil:
		panic(fmt.Sprintf(
			"sparkwing: node %q: Work returned a typed step of type %v but the job struct does not embed "+
				"sparkwing.Produces[%v] (add the marker so the contract is visible at the type level)",
			id, workType, workType,
		))
	case hasMarker && workType != nil:
		declared := pr.producedType()
		if declared != workType {
			panic(fmt.Sprintf(
				"sparkwing: node %q: Produces[%v] but Work returned a step of type %v (align them)",
				id, declared, workType,
			))
		}
		outType = declared
	}

	n := &JobNode{
		id:         id,
		job:        job,
		work:       w,
		resultStep: resultStep,
		outType:    outType,
	}
	for _, r := range collectRefs(job) {
		if r.Pipeline == "" {
			n.addNeed(r.NodeID)
		}
	}
	return n
}

// Job registers a [Workable] or func(context.Context) error under id.
// The inner DAG is materialized at registration.
// A typed-output job must embed [Produces] and return a WorkStep whose function
// returns (T, error). Missing either part panics during registration.
// Panics if the plan is nil, id is empty or registered, or the job has an invalid type.
func Job(p *Plan, id string, x any) *JobNode {
	if p == nil {
		panic("sparkwing: Job: plan must be non-nil")
	}
	if _, ok := p.byID[id]; ok {
		panic(fmt.Sprintf("sparkwing: Job: duplicate node id %q", id))
	}
	job := coerceJobArg("Job", id, x)
	registerJobArgs(p, id, x)
	n := newNode("Job", id, job)
	n.diagnostics = p.diagnostics
	p.nodes = append(p.nodes, n)
	p.byID[id] = n
	return n
}

func coerceJobArg(caller, id string, x any) Workable {
	switch v := x.(type) {
	case nil:
		panic(fmt.Sprintf("sparkwing: %s(%q): job must be non-nil", caller, id))
	case Workable:
		return v
	case func(ctx context.Context) error:
		return &jobFn{fn: v}
	default:
		panic(fmt.Sprintf(
			"sparkwing: %s(%q): third argument must be sparkwing.Workable or "+
				"func(ctx context.Context) error, got %T",
			caller, id, x,
		))
	}
}

func coerceRecoveryArg(caller, id string, x any) Workable {
	switch v := x.(type) {
	case FailureRecoveryFn:
		return &recoveryFn{fn: v}
	case func(ctx context.Context, f Failure) error:
		return &recoveryFn{fn: v}
	default:
		return coerceJobArg(caller, id, x)
	}
}

func materializeWork(id string, job Workable) (*Work, *WorkStep) {
	defer func() {
		if r := recover(); r != nil {
			panic(fmt.Errorf("sparkwing: Plan-time materialization failed for node %q: %w", id, planningFailure(r)))
		}
	}()
	w := NewWork()
	resultStep, err := job.Work(w)
	if err != nil {
		panic(err)
	}
	return w, resultStep
}

// JobNode is a [Plan] entry wrapping a [Workable].
// Inner-DAG modifiers belong to [WorkStep].
type JobNode struct {
	diagnostics                                                         *planningDiagnostics
	memoKey                                                             MemoKeyFn
	memoOptions                                                         memoOptions
	declaredEnv                                                         map[string]nodeproto.EnvValue
	generators                                                          []*Generator
	needsGenerated                                                      []string
	approvalSpec                                                        *ApprovalConfig
	retrySet, retryBackoffSet, timeoutSet, noProgressSet, skipBudgetSet bool
	id                                                                  string
	job                                                                 Workable
	work                                                                *Work
	resultStep                                                          *WorkStep
	needs                                                               []string
	outType                                                             reflect.Type

	retryAttempts     int
	retryBackoff      time.Duration
	timeout           time.Duration
	noProgressTimeout time.Duration

	onFailure *JobNode

	skipIf        []SkipPredicate
	skipIfTimeout time.Duration

	concurrency *concurrencyMembership

	dirCaches []DirCache

	resources *ResourceHints

	beforeRun []BeforeRunFn
	afterRun  []AfterRunFn

	verify VerifyFn

	requires []string
	tools    []string

	prefers []string
	// children are the pipelines this job may start with RunAndAwait.
	children []nodeproto.ChildPipeline

	inline bool

	continueOnError bool
	optional        bool
	needsOptional   []string

	outputGlobs []string
	consumes    []consumeEdge
}

// ApprovalTimeoutPolicy enumerates the resolution applied to an
// unanswered approval gate when its Timeout elapses.
type ApprovalTimeoutPolicy string

const (
	ApprovalFail    ApprovalTimeoutPolicy = "fail"
	ApprovalDeny    ApprovalTimeoutPolicy = "deny"
	ApprovalApprove ApprovalTimeoutPolicy = "approve"
)

// SkipPredicate is a function evaluated after upstream dependencies complete.
// Executable-job predicates run on the executing host under the actual job
// award, inside its acquired concurrency slot. A true result skips the body
// and releases the job's groups. Approval predicates gate the human wait.
//
// Predicates must be cheap and side-effect-free because their budget is short.
// Long-running logic belongs in a dedicated job feeding typed output via Ref.
type SkipPredicate func(ctx context.Context) bool

// BeforeRunFn runs once before the first Run attempt. A non-nil error
// fails the node immediately; Run does not execute and Retry does
// not apply.
type BeforeRunFn func(ctx context.Context) error

// AfterRunFn receives the final Run error after all retries, or nil on success.
// A hook panic is logged and leaves the node’s outcome unchanged.
type AfterRunFn func(ctx context.Context, err error)

// Dep accepts [JobNode], [Approval], and [JobGroup] handles as dependencies.
type Dep interface {
	depID() string
}

func (n *JobNode) depID() string  { return n.id }
func (g *JobGroup) depID() string { return g.name }

var (
	_ Dep = (*JobNode)(nil)
	_ Dep = (*Approval)(nil)
	_ Dep = (*JobGroup)(nil)
)

func (n *JobNode) ID() string { return n.id }

// Needs declares hard upstream dependencies. The orchestrator will not
// dispatch this node until every named dependency is satisfied.
//
// Accepts any [Dep]: [*JobNode], [*Approval], or [*JobGroup]. For
// multiple typed nodes from a slice, splat the slice:
//
//	n.Needs(upstreams...)
func (n *JobNode) Needs(deps ...Dep) *JobNode {
	for _, d := range deps {
		if d == nil {
			continue
		}
		if g, ok := d.(*JobGroup); ok && g != nil {
			for _, m := range g.members {
				n.addNeed(m.id)
			}
			continue
		}
		if id := d.depID(); id != "" {
			n.addNeed(id)
		}
	}
	return n
}

func (n *JobNode) addNeed(id string) {
	if slices.Contains(n.needs, id) {
		return
	}
	n.needs = append(n.needs, id)
}

// RetryConfig is the retry envelope for a Job. Attempts == 0 means no retry.
type RetryConfig struct {
	Attempts   int
	Backoff    time.Duration
	backoffSet bool
}

// RetryOption tunes a Retry(...) call.
type RetryOption func(*RetryConfig)

// Retry configures the node to be re-attempted up to attempts
// additional times on failure. Zero (the default) means no retry.
// Cancelled nodes are never retried.
//
//	sw.Job(plan, "flaky-test", &Test{}).Retry(2)
//	sw.Job(plan, "flaky-test", &Test{}).Retry(3, sw.Backoff(500*time.Millisecond))
func (n *JobNode) Retry(attempts int, opts ...RetryOption) *JobNode {
	cfg := RetryConfig{Attempts: attempts}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	n.retrySet = true
	n.retryAttempts = cfg.Attempts
	n.retryBackoff = cfg.Backoff
	n.retryBackoffSet = cfg.backoffSet
	return n
}

// Timeout caps the per-attempt duration. Exceeding the timeout
// cancels the job's context, returns an ExecError, and is treated
// as a normal failure (eligible for retries).
func (n *JobNode) Timeout(d time.Duration) *JobNode {
	n.timeoutSet = true
	n.timeout = d
	return n
}

// NoProgressTimeout caps how long an attempt may run without emitting a
// node log record. The timer resets on each record and is suspended while
// the node waits for delegated child work or tool admission. Zero disables
// the timeout.
func (n *JobNode) NoProgressTimeout(d time.Duration) *JobNode {
	n.noProgressSet = true
	n.noProgressTimeout = d
	return n
}

// OnFailure declares and returns a recovery job that runs when this job fails.
func (n *JobNode) OnFailure(id string, x any) *JobNode {
	job := coerceRecoveryArg("OnFailure", id, x)
	n.diagnostics.collectJob(job)
	n.onFailure = newNode("OnFailure", id, job)
	n.onFailure.diagnostics = n.diagnostics
	return n.onFailure
}

// SkipOption configures a SkipIf registration.
type SkipOption func(*JobNode)

// SkipBudget sets the timeout applied to each predicate on the node.
// The last registration wins; zero uses the orchestrator’s timeout.
func SkipBudget(d time.Duration) SkipOption {
	return func(n *JobNode) {
		n.skipIfTimeout = d
		n.skipBudgetSet = true
	}
}

// SkipIf registers a predicate evaluated on the executing host after this
// node's dependencies complete and its concurrency groups are acquired.
// Evaluation uses the node's actual execution award. If a predicate returns
// true, the node is marked Skipped and releases its groups without running
// its body. A skipped node satisfies dependencies, so its dependents can run.
// Apply the predicate to those dependents or their JobGroup to skip them too.
//
// Typed upstream output is consumed via closure capture + Ref.Get:
//
//	setup := sw.Job(plan, "setup", &Setup{})
//	setupOut := sw.RefTo[SetupOutput](setup)
//	sw.Job(plan, "deploy", &Deploy{}).
//	    Needs(setup).
//	    SkipIf(func(ctx context.Context) bool {
//	        return setupOut.Get(ctx).SkipDeploy
//	    })
//
// Multiple SkipIf calls accumulate with OR semantics: any true result
// skips the node. A node that skips still waits for its concurrency slot
// and briefly occupies a machine. Predicates have a default 30s budget;
// pass SkipBudget(d) to override:
//
//	deploy.SkipIf(pred, sparkwing.SkipBudget(2*time.Minute))
func (n *JobNode) SkipIf(fn SkipPredicate, opts ...SkipOption) *JobNode {
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

// BeforeRun registers a hook to run once before the node's Run method
// on the first attempt. A non-nil error fails the node without
// invoking Run and without retrying.
func (n *JobNode) BeforeRun(fn BeforeRunFn) *JobNode {
	if fn != nil {
		n.beforeRun = append(n.beforeRun, fn)
	}
	return n
}

// AfterRun registers a hook invoked after Run and all retries finish.
// A hook panic is logged and leaves the node’s outcome unchanged.
func (n *JobNode) AfterRun(fn AfterRunFn) *JobNode {
	if fn != nil {
		n.afterRun = append(n.afterRun, fn)
	}
	return n
}

// Verify checks a postcondition after each successful action attempt.
// An error fails the node at [StageVerify] and follows Retry and OnFailure.
// Retries repeat both the action and its check. A cache hit skips both.
// A check that must run every time belongs in its own uncached node.
func (n *JobNode) Verify(fn VerifyFn) *JobNode {
	n.verify = fn
	return n
}

// Requires records label terms used to filter runner claims for non-inline dispatched jobs.
// Each argument is one term; within a term,
// comma-separated values are alternatives (OR). Across terms, terms
// compose with AND. A runner advertising a superset of the matched
// alternatives still matches.
//
// Labels are equality strings; common conventions are bare tags
// ("laptop", "gpu") or key=value ("arch=arm64"). Calling Requires
// with no arguments clears any previously-set labels.
//
//	sw.Job(plan, "train",      &Train{}).Requires("gpu")
//	sw.Job(plan, "package",    &Package{}).Requires("arch=arm64", "trusted")
//	sw.Job(plan, "build-x",    &Build{}).Requires("os=linux,macos")        // OR
//	sw.Job(plan, "build-y",    &Build{}).Requires("os=linux,macos", "amd64") // (linux OR macos) AND amd64
//
// An unmatched non-inline dispatched job remains queued until the controller
// fails it with queue_timeout. Direct runs and inline jobs have no runner claim
// step, so Requires does not select or reject their runner. For conditional
// For a soft preference, use Prefers.
func (n *JobNode) Requires(labels ...string) *JobNode {
	n.requires = normalizeLabels(labels)
	return n
}

// StartsChildren appends pipelines in the parent repository that this job may start with
// RunAndAwait. The controller refuses a child the job did not declare, so a job
// that runs untrusted code cannot start a pipeline its plan never named.
//
//	sw.Job(plan, "release", &Release{}).StartsChildren("deploy-us", "deploy-eu")
func (n *JobNode) StartsChildren(pipelines ...string) *JobNode {
	return n.StartsChildrenIn("", pipelines...)
}

// StartsChildrenIn declares child pipelines in a canonical repository key such as
// "gh:123" or "git:host/org/repo". An empty repository inherits the parent repository.
func (n *JobNode) StartsChildrenIn(repository string, pipelines ...string) *JobNode {
	for _, pipeline := range pipelines {
		n.children = append(n.children, nodeproto.ChildPipeline{Pipeline: pipeline, Repository: repository})
	}
	return n
}

// Prefers ranks eligible runners without relaxing requirements, reservations or resource bounds.
func (n *JobNode) Prefers(labels ...string) *JobNode {
	n.prefers = normalizeLabels(labels)
	return n
}

func normalizeLabels(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, l := range in {
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Inline places the node on the dispatcher's host.
// Local execution uses a separate process; cluster execution uses the dispatcher's
// own goroutine pool, where CPU-heavy work delays other nodes.
// Retry, timeout, and memoization still apply. Requires labels are ignored.
func (n *JobNode) Inline() *JobNode {
	if n.approvalSpec != nil {
		panic(fmt.Sprintf("sparkwing: Job.Inline: approval gate %q cannot be inlined; approvals are long-lived by design", n.id))
	}
	n.inline = true
	return n
}

// ContinueOnError tells the orchestrator that downstream dependents
// should proceed even when this node fails. The run as a whole still
// reports as failed (use Optional to also suppress run-level failure).
func (n *JobNode) ContinueOnError() *JobNode {
	n.continueOnError = true
	return n
}

// Optional marks the node as non-essential: a failure is logged as a
// warning and does not count toward the run's overall success/fail
// outcome. Implies ContinueOnError.
func (n *JobNode) Optional() *JobNode {
	n.optional = true
	n.continueOnError = true
	return n
}
