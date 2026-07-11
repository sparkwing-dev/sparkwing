package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// LocalAdmission wires a run onto the local admission daemon
// (sparkwingd). The local entry points -- the pipeline binary's run
// path and handle-trigger --local -- construct one; cluster paths and
// unit tests leave Options.Admission nil and take no daemon dependency.
// Admission belongs to whoever owns the machine: sparkwingd arbitrates
// a laptop it owns, while in-cluster work was already admitted by the
// Kubernetes scheduler and must never engage the daemon.
//
// At run start the orchestrator submits one all-or-nothing admission
// request composed from the plan (host resources plus every box- and
// run-scoped plan-level concurrency group) and blocks on the pushed
// grant. The lease is held by the open daemon connection for the run's
// whole lifetime and released after the run row is finalized. A child
// run carrying ParentLeaseToken attaches to the parent's lease instead
// of re-admitting.
type LocalAdmission struct {
	// Home is the sparkwing home whose daemon arbitrates. Empty resolves
	// the default ($SPARKWING_HOME or ~/.sparkwing).
	Home string
	// Version is this binary's version, used for the daemon version
	// handshake and newer-client takeover. Empty never triggers takeover.
	Version string
	// ParentLeaseToken, when non-empty, attaches this run to the parent
	// run's live lease (zero additional host budget) instead of
	// submitting a fresh admission request.
	ParentLeaseToken string
	// Stderr receives the single-line queue status while the run waits
	// for admission. Nil uses os.Stderr.
	Stderr io.Writer
	// Spawn overrides how a missing daemon is started. Nil uses the
	// default, which re-execs this binary as `wingd run`. Tests inject
	// an in-process daemon here.
	Spawn func(home, version string) error
	// DialTimeout and Backoff tune the client's connect loop; zero uses
	// the client defaults.
	DialTimeout time.Duration
	Backoff     time.Duration
}

func (la *LocalAdmission) clientOptions() wingdclient.Options {
	return wingdclient.Options{
		Home:        la.Home,
		Version:     la.Version,
		Spawn:       la.Spawn,
		DialTimeout: la.DialTimeout,
		Backoff:     la.Backoff,
	}
}

func (la *LocalAdmission) stderr() io.Writer {
	if la.Stderr != nil {
		return la.Stderr
	}
	return os.Stderr
}

// localPlanSemsID suffixes the participant ID a child run uses for the
// semaphores its plan claims beyond what the inherited parent lease
// already holds.
const localPlanSemsID = "/plan"

// runLease is the granted local admission a run holds until terminal:
// the main lease (fresh grant or child attach) and, for a child whose
// plan claims semaphores the parent lease does not hold, the extra
// semaphores-only lease.
type runLease struct {
	token  string
	leases []*wingdclient.Lease
}

// release returns every held lease and closes the daemon connections.
// Call it only after the run row is finalized, so the daemon's orphan
// finalizer can never race a still-running row.
func (rl *runLease) release() {
	if rl == nil {
		return
	}
	for _, l := range rl.leases {
		_ = l.Release()
	}
}

// admitOutcome classifies a terminal admission answer that short-
// circuits the run without dispatching.
type admitOutcome string

const (
	admitProceed admitOutcome = ""
	admitSkipped admitOutcome = "skip"
)

// admitRun submits the run's admission request and blocks until the
// daemon grants it. While queued it renders position updates on stderr
// and appends admission_wait events to the run row. onEvicted is
// invoked (once, from a watcher goroutine) when the daemon later evicts
// the granted lease under a cancel_others requester.
func (la *LocalAdmission) admitRun(
	ctx context.Context,
	backends Backends,
	runID string,
	plan *sparkwing.Plan,
	workers int,
	onEvicted func(error),
) (*runLease, admitOutcome, error) {
	if la.ParentLeaseToken != "" {
		return la.attachChildRun(ctx, backends, runID, plan, onEvicted)
	}
	req := wingwire.AdmissionRequest{
		RunID:      runID,
		Resources:  hostResourcesForPlan(plan, workers),
		Semaphores: planSemaphoreClaims(plan, runID),
	}
	lease, outcome, err := la.acquireBlocking(ctx, backends, runID, req)
	if err != nil || outcome != admitProceed {
		return nil, outcome, err
	}
	rl := &runLease{token: lease.Token, leases: []*wingdclient.Lease{lease}}
	go lease.Watch(evictionHandler(runID, onEvicted))
	return rl, admitProceed, nil
}

// attachChildRun joins the parent's live lease (zero budget), then
// acquires any plan-level semaphores the parent lease does not already
// hold through a second, semaphores-only request.
func (la *LocalAdmission) attachChildRun(
	ctx context.Context,
	backends Backends,
	runID string,
	plan *sparkwing.Plan,
	onEvicted func(error),
) (*runLease, admitOutcome, error) {
	cl, err := wingdclient.EnsureDaemon(ctx, la.clientOptions())
	if err != nil {
		return nil, admitProceed, fmt.Errorf("local admission: %w", err)
	}
	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID:            runID,
		ParentLeaseToken: la.ParentLeaseToken,
	}, nil)
	if err != nil {
		cl.Close()
		return nil, admitProceed, fmt.Errorf("local admission: attach to parent lease: %w", err)
	}
	rl := &runLease{token: lease.Token, leases: []*wingdclient.Lease{lease}}
	go lease.Watch(evictionHandler(runID, onEvicted))

	inherited := make(map[string]bool, len(lease.Semaphores))
	for _, name := range lease.Semaphores {
		inherited[name] = true
	}
	var extra []wingwire.SemaphoreClaim
	for _, claim := range planSemaphoreClaims(plan, runID) {
		if !inherited[claim.Name] {
			extra = append(extra, claim)
		}
	}
	if len(extra) == 0 {
		return rl, admitProceed, nil
	}
	extraLease, outcome, err := la.acquireBlocking(ctx, backends, runID, wingwire.AdmissionRequest{
		RunID:          runID + localPlanSemsID,
		SemaphoresOnly: true,
		Semaphores:     extra,
	})
	if err != nil || outcome != admitProceed {
		rl.release()
		return nil, outcome, err
	}
	rl.leases = append(rl.leases, extraLease)
	go extraLease.Watch(evictionHandler(runID, onEvicted))
	return rl, admitProceed, nil
}

// acquireBlocking connects to the daemon and blocks on one admission
// request, translating policy outcomes: skip short-circuits the run,
// fail and eviction become named errors, and a queue timeout declared
// by the smallest-bounded claim converts the wait into an error naming
// the key.
func (la *LocalAdmission) acquireBlocking(
	ctx context.Context,
	backends Backends,
	runID string,
	req wingwire.AdmissionRequest,
) (*wingdclient.Lease, admitOutcome, error) {
	acquireCtx := ctx
	if key, timeout := tightestQueueTimeout(req.Semaphores); timeout > 0 {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeoutCause(ctx, timeout,
			fmt.Errorf("plan concurrency group %q: queued %s without a slot under OnLimit:Queue", key, timeout))
		defer cancel()
	}
	cl, err := wingdclient.EnsureDaemon(acquireCtx, la.clientOptions())
	if err != nil {
		return nil, admitProceed, fmt.Errorf("local admission: %w", err)
	}
	waited := false
	lease, err := cl.Acquire(acquireCtx, req, func(q wingwire.Queued) {
		waited = true
		la.reportQueued(ctx, backends, runID, q)
	})
	if err != nil {
		cl.Close()
		if cause := context.Cause(acquireCtx); cause != nil && ctx.Err() == nil {
			appendPlanEvent(ctx, backends, runID, "admission_queue_timeout", nil)
			return nil, admitProceed, cause
		}
		var admErr *wingdclient.AdmissionError
		if errors.As(err, &admErr) {
			switch admErr.Policy {
			case wingwire.PolicySkip:
				appendPlanEvent(ctx, backends, runID, "plan_skipped_concurrent", nil)
				return nil, admitSkipped, nil
			case wingwire.PolicyFail:
				appendPlanEvent(ctx, backends, runID, "plan_failed_concurrent", nil)
				return nil, admitProceed, admissionFailure(admErr)
			}
			return nil, admitProceed, fmt.Errorf("local admission: %w", admErr)
		}
		return nil, admitProceed, fmt.Errorf("local admission: %w", err)
	}
	if waited {
		appendPlanEvent(ctx, backends, runID, "admission_granted", nil)
		fmt.Fprintf(la.stderr(), "admitted; starting run\n")
	}
	return lease, admitProceed, nil
}

// reportQueued renders one queue-position update: a single stderr line
// plus an admission_wait event on the run row.
func (la *LocalAdmission) reportQueued(ctx context.Context, backends Backends, runID string, q wingwire.Queued) {
	ahead := q.Position - 1
	if ahead < 0 {
		ahead = 0
	}
	noun := "runs"
	if ahead == 1 {
		noun = "run"
	}
	fmt.Fprintf(la.stderr(),
		"queued for local admission: position %d of %d (%d %s ahead)\n",
		q.Position, q.QueueLength, ahead, noun)
	payload := fmt.Appendf(nil, `{"position":%d,"queue_length":%d}`, q.Position, q.QueueLength)
	appendPlanEvent(ctx, backends, runID, "admission_wait", payload)
}

// admissionFailure maps a terminal fail-policy answer to a named error.
func admissionFailure(admErr *wingdclient.AdmissionError) error {
	switch admErr.Key {
	case "never_admissible":
		return errors.New("local admission: requested resources exceed this machine's total capacity")
	case "duplicate", "invalid", "parent", "reattach":
		return fmt.Errorf("local admission: %w", admErr)
	default:
		return fmt.Errorf("plan concurrency group %q: slot full under OnLimit:Fail", admErr.Key)
	}
}

// evictionHandler adapts a daemon eviction push into the run-cancelling
// error carrying the key, policy, and superseding run.
func evictionHandler(runID string, onEvicted func(error)) func(wingwire.Evicted) {
	return func(ev wingwire.Evicted) {
		if onEvicted == nil {
			return
		}
		onEvicted(&planAdmissionEvictedError{
			groupName:    ev.Key,
			policy:       string(ev.Policy),
			supersededBy: ev.SupersededBy,
			runID:        runID,
		})
	}
}

// tightestQueueTimeout returns the smallest non-zero queue timeout among
// queue-policy claims and the key that declared it.
func tightestQueueTimeout(claims []wingwire.SemaphoreClaim) (string, time.Duration) {
	var key string
	var timeout time.Duration
	for _, c := range claims {
		if c.QueueTimeoutMS <= 0 || (c.Policy != "" && c.Policy != wingwire.PolicyQueue) {
			continue
		}
		d := time.Duration(c.QueueTimeoutMS) * time.Millisecond
		if timeout == 0 || d < timeout {
			key, timeout = c.Name, d
		}
	}
	return key, timeout
}

// hostResourcesForPlan composes the run's host charge: the plan-level
// Resources() hints when declared, else the largest node-level hint,
// else a conservative default of min(workers, half the machine's cores)
// so an unhinted run occupies real capacity without ever exceeding what
// the daemon's reserved headroom can grant.
func hostResourcesForPlan(plan *sparkwing.Plan, workers int) wingwire.HostResources {
	if rh := plan.ResourceHints(); rh != nil && (rh.Cores > 0 || rh.MemoryBytes > 0) {
		return wingwire.HostResources{Cores: rh.Cores, MemoryBytes: rh.MemoryBytes}
	}
	var cores float64
	var mem int64
	for _, n := range plan.Nodes() {
		if h := n.ResourceHints(); h != nil {
			cores = math.Max(cores, h.Cores)
			mem = max(mem, h.MemoryBytes)
		}
	}
	if cores > 0 || mem > 0 {
		return wingwire.HostResources{Cores: cores, MemoryBytes: mem}
	}
	return wingwire.HostResources{Cores: defaultRunCores(workers)}
}

// defaultRunCores is the conservative host charge for a run with no
// resource hints: the dispatcher's worker cap, bounded by half the
// machine so the charge always fits under the daemon's reserved
// headroom, and never below one core.
func defaultRunCores(workers int) float64 {
	half := math.Ceil(float64(runtime.NumCPU()) / 2)
	cores := half
	if workers > 0 {
		cores = math.Min(float64(workers), half)
	}
	return math.Max(1, cores)
}

// planSemaphoreClaims maps the plan-level Concurrency() groups with
// box or run scope onto wire semaphore claims. Global-scope groups are
// excluded: they pool across the fleet through the shared store, not
// the local daemon.
func planSemaphoreClaims(plan *sparkwing.Plan, runID string) []wingwire.SemaphoreClaim {
	var claims []wingwire.SemaphoreClaim
	seen := map[string]bool{}
	for _, membership := range plan.PlanConcurrency() {
		group := membership.Group
		if group == nil || !groupUsesLocalDaemon(group) {
			continue
		}
		key := scopedGroupKey(group, runID)
		if seen[key] {
			continue
		}
		seen[key] = true
		limit := group.Limit()
		claims = append(claims, wingwire.SemaphoreClaim{
			Name:           key,
			Cost:           membership.Cost,
			Capacity:       limit.Capacity,
			Policy:         wingwire.Policy(limit.OnLimit),
			QueueTimeoutMS: limit.QueueTimeout.Milliseconds(),
		})
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].Name < claims[j].Name })
	return claims
}

// groupUsesLocalDaemon reports whether a concurrency group's scope is
// arbitrated by the local daemon (box and run scope) rather than the
// shared store (global scope).
func groupUsesLocalDaemon(group *sparkwing.ConcurrencyGroup) bool {
	switch group.Limit().Scope {
	case sparkwing.ScopeBox, sparkwing.ScopeRun:
		return true
	default:
		return false
	}
}

// localAdmissionCtxKey carries the run's LocalAdmission and lease token
// through the dispatch context, so node-level semaphore acquisitions
// reach the same daemon and spawned children inherit the lease.
type localAdmissionCtxKey struct{}

type localAdmissionState struct {
	la    *LocalAdmission
	token string
}

func withLocalAdmission(ctx context.Context, la *LocalAdmission, leaseToken string) context.Context {
	if la == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, localAdmissionCtxKey{}, localAdmissionState{la: la, token: leaseToken})
	if leaseToken != "" {
		ctx = sparkwing.WithCommandEnv(ctx, map[string]string{wingwire.LeaseTokenEnv: leaseToken})
	}
	return ctx
}

func localAdmissionFromContext(ctx context.Context) (*LocalAdmission, string) {
	state, ok := ctx.Value(localAdmissionCtxKey{}).(localAdmissionState)
	if !ok {
		return nil, ""
	}
	return state.la, state.token
}

// leaseTriggerEnv is the env a parent stamps onto spawned child
// triggers so the child attaches to the parent's lease. Nil when the
// run holds no daemon lease.
func leaseTriggerEnv(ctx context.Context) map[string]string {
	_, token := localAdmissionFromContext(ctx)
	if token == "" {
		return nil
	}
	return map[string]string{wingwire.LeaseTokenEnv: token}
}

// acquireNodeSlot submits one short-lived, semaphores-only admission
// request for a node-level concurrency group and blocks until granted.
// The returned lease is released at node end; its Watch surfaces a
// cancel_others eviction while the node runs.
func (la *LocalAdmission) acquireNodeSlot(
	ctx context.Context,
	runID, nodeID string,
	claim wingwire.SemaphoreClaim,
	onQueued func(wingwire.Queued),
) (*wingdclient.Lease, error) {
	cl, err := wingdclient.EnsureDaemon(ctx, la.clientOptions())
	if err != nil {
		return nil, fmt.Errorf("local admission: %w", err)
	}
	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID:          runID + "/" + nodeID,
		SemaphoresOnly: true,
		Semaphores:     []wingwire.SemaphoreClaim{claim},
	}, onQueued)
	if err != nil {
		cl.Close()
		return nil, err
	}
	return lease, nil
}

// childQueueStatus reports whether a spawned child run is currently
// queued in the local daemon (its own admission request or its extra
// plan-semaphores request), so the parent's node timeout can exclude
// the time the child spends waiting for admission.
func (la *LocalAdmission) childQueueStatus(ctx context.Context, childRunID string) (childPlanAdmission, error) {
	qs, err := wingdclient.Query(ctx, la.clientOptions())
	if err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			return childPlanAdmission{Status: childPlanAdmissionAdmitted}, nil
		}
		return childPlanAdmission{Status: childPlanAdmissionUnknown}, err
	}
	for _, w := range qs.Waiters {
		if w.RunID != childRunID && w.RunID != childRunID+localPlanSemsID {
			continue
		}
		queuedAt := time.Now()
		if w.WaitingMS > 0 {
			queuedAt = queuedAt.Add(-time.Duration(w.WaitingMS) * time.Millisecond)
		}
		return childPlanAdmission{Status: childPlanAdmissionQueued, QueuedAt: queuedAt}, nil
	}
	return childPlanAdmission{Status: childPlanAdmissionAdmitted}, nil
}

// childAdmissionStatus resolves a child run's admission state for the
// parent's timeout accounting. With no local daemon in play (cluster
// paths) it is the store-driven check over every plan concurrency key;
// on the local path the daemon answers for box- and run-scoped keys
// while the store still answers for global ones.
func childAdmissionStatus(
	ctx context.Context,
	state StateBackend,
	concurrency ConcurrencyBackend,
	la *LocalAdmission,
	childRunID string,
) (childPlanAdmission, error) {
	if la == nil {
		return childPlanAdmissionStatusForRun(ctx, state, concurrency, childRunID)
	}
	daemonStatus, err := la.childQueueStatus(ctx, childRunID)
	if err != nil || daemonStatus.Status == childPlanAdmissionQueued {
		return daemonStatus, err
	}
	storeStatus, err := childPlanAdmissionStatusForGlobalKeys(ctx, state, concurrency, childRunID)
	if err != nil {
		return storeStatus, err
	}
	if storeStatus.Status == childPlanAdmissionQueued {
		return storeStatus, nil
	}
	return childPlanAdmission{Status: childPlanAdmissionAdmitted}, nil
}

// sparkwingModuleVersion reports the SDK module version compiled into
// this binary, used for the daemon version handshake. Empty when build
// info is unavailable, which disables version takeover.
func sparkwingModuleVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	const modulePath = "github.com/sparkwing-dev/sparkwing"
	if bi.Main.Path == modulePath {
		return bi.Main.Version
	}
	for _, dep := range bi.Deps {
		if dep.Path == modulePath {
			return dep.Version
		}
	}
	return ""
}
