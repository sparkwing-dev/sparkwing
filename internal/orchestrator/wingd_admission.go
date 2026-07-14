package orchestrator

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
// Run-level admission holds explicit whole-run resources and plan-level
// semaphores. Unpinned local runs acquire host resources per node, so a cheap
// ready node is not queued behind a later expensive node in the same DAG. A
// child run carrying ParentLeaseToken attaches to the parent's lease instead
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
	// Origin names who dispatched the run this admission covers -- the
	// operator's own local work or a controller that sent it to a
	// registered runner on this box. Empty is treated as local; the
	// runner-mode path sets it to [wingwire.OriginController] so the shared
	// daemon's queue attributes contended work to whoever launched it.
	Origin wingwire.Origin
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
	// QueueHeartbeat is how often a still-queued run re-emits its wait
	// status on stderr so a long admission wait never reads as a hang.
	// Zero uses defaultQueueHeartbeat.
	QueueHeartbeat time.Duration
}

// defaultQueueHeartbeat is the re-emit cadence for a queued run's wait
// status when [LocalAdmission.QueueHeartbeat] is unset.
const defaultQueueHeartbeat = 30 * time.Second

func (la *LocalAdmission) heartbeatInterval() time.Duration {
	if la.QueueHeartbeat > 0 {
		return la.QueueHeartbeat
	}
	return defaultQueueHeartbeat
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

// contentionAttribution asks the daemon, before the run's lease is
// released, whether it flagged this run as throttled by host contention.
// When it did, it returns a one-line end-of-run attribution comparing the
// run's duration to its measured p50 and naming the host-saturation share.
// It returns "" when no daemon answers, the run is not flagged, or there
// is no measured baseline to compare against -- never a fabricated verdict.
func (la *LocalAdmission) contentionAttribution(ctx context.Context, runID string) string {
	qs, err := wingdclient.Query(ctx, la.clientOptions())
	if err != nil {
		return ""
	}
	for _, h := range qs.Holders {
		if h.RunID != runID || !h.Contended {
			continue
		}
		sat := int(h.SaturatedShare*100 + 0.5)
		if h.ExpectedDurationMS > 0 {
			return fmt.Sprintf("took %s vs p50 %s; host saturated %d%% of the run",
				fmtAdmissionDur(h.ElapsedMS), fmtAdmissionDur(h.ExpectedDurationMS), sat)
		}
		return h.ContentionReason
	}
	return ""
}

// fmtAdmissionDur renders a millisecond duration to the nearest second for
// the end-of-run contention attribution.
func fmtAdmissionDur(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
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
	token        string
	hostAdmitted bool
	leases       []*wingdclient.Lease
	// driftWarning, when set, is the one-line note that this run's explicit
	// pin has drifted from its measured profile, surfaced at run end.
	driftWarning string
	// charge is the host cost this run was admitted at, carried so a
	// contended run's end-of-run fold can recognize a ceiling hit. Zero for a
	// child run that attached to its parent's lease.
	charge runCharge
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
	pipeline string,
	runID string,
	plan *sparkwing.Plan,
	workers int,
	onEvicted func(error),
) (*runLease, admitOutcome, error) {
	if la.ParentLeaseToken != "" {
		return la.attachChildRun(ctx, backends, runID, pipeline, plan, onEvicted)
	}
	var res capacity.Resolution
	var prof *store.PipelineProfile
	var drift *capacity.Drift
	var overCap string
	hostPinned := planHasResourcePin(plan)
	if hostPinned {
		res, prof, drift, overCap = la.resolveHostCost(ctx, backends, pipeline, plan)
	}
	req := wingwire.AdmissionRequest{
		RunID:              runID,
		Pipeline:           pipeline,
		Repo:               currentRepoShortName(),
		PID:                os.Getpid(),
		Resources:          wingwire.HostResources{Cores: res.Cores, MemoryBytes: res.MemoryBytes},
		Semaphores:         planSemaphoreClaims(plan, runID),
		SemaphoresOnly:     !hostPinned,
		CostSource:         wingwire.CostSource(res.Source),
		ExpectedDurationMS: res.ExpectedDuration.Milliseconds(),
		Origin:             la.Origin,
	}
	if prof != nil {
		req.ExpectedP99MS = prof.P99Duration.Milliseconds()
		req.SampleCount = prof.SampleCount
	}
	warning := hostChargeWarning(drift, overCap)
	if warning != "" {
		req.DriftWarning = warning
		fmt.Fprintln(la.stderr(), warning)
	}
	if note := measuringNarration(res, prof); note != "" {
		fmt.Fprintln(la.stderr(), note)
	}
	submitted := time.Now()
	lease, outcome, err := la.acquireBlocking(ctx, backends, runID, req)
	if err != nil || outcome != admitProceed {
		return nil, outcome, err
	}
	if st := canonicalLocalStore(backends.State); st != nil && pipeline != "" {
		_ = st.RecordWaitObservation(ctx, pipeline, time.Since(submitted))
	}
	rl := &runLease{token: lease.Token, hostAdmitted: hostPinned, leases: []*wingdclient.Lease{lease}}
	rl.driftWarning = warning
	rl.charge = runCharge{Cores: res.Cores, MemoryBytes: res.MemoryBytes}
	if lease.SoleRunUnderLoad {
		fmt.Fprintf(la.stderr(),
			"admitted as sole run; host under external load %.1f cores - additional runs will queue\n",
			lease.ExternalCores)
	}
	go lease.WatchControl(evictionHandler(runID, onEvicted), cancelHandler(onEvicted))
	return rl, admitProceed, nil
}

func planHasResourcePin(plan *sparkwing.Plan) bool {
	if plan == nil {
		return false
	}
	h := plan.ResourceHints()
	return h != nil && (h.Cores > 0 || h.MemoryBytes > 0)
}

// measuringNarration is the one-line note shown when a run is charged by
// measurement rather than a finalized measured price: a version being
// re-measured after a structural change (charged a safety multiple of the
// prior peak), or one priced from the demand floor its contended runs
// proved. It returns "" for the finalized measured, pin, and cold-start
// default sources, which the queue view already labels.
func measuringNarration(res capacity.Resolution, prof *store.PipelineProfile) string {
	switch res.Source {
	case store.CostSourceMeasuring:
		return fmt.Sprintf("re-measuring at %.1f cores (2x prior charge); runs under contention only raise the floor, clean runs finalize the price",
			res.Cores)
	case store.CostSourceFloor:
		n := 0
		if prof != nil {
			n = prof.ContendedCount
		}
		return fmt.Sprintf("measuring at %.1f cores from the demand floor of %d contended run(s); a clean run finalizes the price",
			res.Cores, n)
	default:
		return ""
	}
}

// hostChargeWarning picks the one-line note shown for a run's host charge: an
// over-capacity pin warning takes precedence over a drift warning, since a pin
// the machine cannot honor verbatim is the more urgent thing to surface.
func hostChargeWarning(drift *capacity.Drift, overCapacity string) string {
	if overCapacity != "" {
		return overCapacity
	}
	if drift != nil {
		return drift.Message
	}
	return ""
}

// admitNode submits one host-resource admission request for a single
// node claimed from a controller and blocks until the local daemon grants
// it. It is the runner-mode counterpart of admitRun: a box that both runs
// local pipelines and serves a controller routes controller-dispatched
// nodes through the same daemon and queue as its own work, tagged with
// [wingwire.OriginController]. The charge is resolved from the node's own
// .Resources() pin, else the measured profile, else the conservative
// default; the node draws no plan semaphores, so its at-limit behaviour is
// always FIFO queueing. The returned lease is held until node end.
func (la *LocalAdmission) admitNode(
	ctx context.Context,
	backends Backends,
	pipeline, runID, nodeID string,
	node *sparkwing.JobNode,
) (*runLease, error) {
	res, _, _, overCap := la.resolveNodeHostCost(ctx, backends, pipeline, nodeID, node)
	req := wingwire.AdmissionRequest{
		RunID:              nodeHostRunID(runID, nodeID),
		Pipeline:           pipeline,
		Repo:               currentRepoShortName(),
		PID:                os.Getpid(),
		Resources:          wingwire.HostResources{Cores: res.Cores, MemoryBytes: res.MemoryBytes},
		CostSource:         wingwire.CostSource(res.Source),
		ExpectedDurationMS: res.ExpectedDuration.Milliseconds(),
		Origin:             la.Origin,
		DriftWarning:       overCap,
		SubLease:           true,
	}
	lease, outcome, err := la.acquireBlocking(ctx, backends, runID, req)
	if err != nil || outcome != admitProceed {
		return nil, err
	}
	rl := &runLease{token: lease.Token, hostAdmitted: leaseCarriesHost(lease), leases: []*wingdclient.Lease{lease}}
	return rl, nil
}

// resolveNodeHostCost resolves the host charge and provenance for one
// node: its explicit .Resources() pin wins, else the node's measured
// profile once it has enough samples, else the conservative default. A
// missing local store (the common runner-mode case, where state is a
// controller client) simply means no measured profile, so the pin-or-
// default order still holds.
func (la *LocalAdmission) resolveNodeHostCost(ctx context.Context, backends Backends, pipeline, nodeID string, node *sparkwing.JobNode) (capacity.Resolution, *store.PipelineProfile, *capacity.Drift, string) {
	pin := nodePin(node)
	var profile *store.PipelineProfile
	if st := canonicalLocalStore(backends.State); st != nil && pipeline != "" {
		if p, err := st.GetPipelineProfile(ctx, pipeline, nodeID); err == nil {
			profile = p
		}
	}
	res := capacity.Resolve(pin, profile, runtime.NumCPU(), "")
	res, overCap := la.applyHostCeiling(ctx, res)
	return res, profile, capacity.CheckDrift(pin, profile), overCap
}

// applyHostCeiling caps a resolved host charge at the daemon's idle grantable
// ceiling so an oversized cost runs alone rather than being rejected, and
// returns the loud warning when an explicit pin is what was capped. It asks
// the running daemon for the ceiling; when none answers the charge is left
// unclamped and the daemon caps it at submit.
func (la *LocalAdmission) applyHostCeiling(ctx context.Context, res capacity.Resolution) (capacity.Resolution, string) {
	machineCores, grantCores, grantMem, ok := la.idleGrantableHost(ctx)
	if !ok {
		return res, ""
	}
	return capacity.ApplyHostCeiling(res, machineCores, grantCores, grantMem)
}

// idleGrantableHost asks the running daemon for the largest host charge it
// grants a single run on an otherwise idle box -- machine capacity minus the
// reserved margin -- returning the machine size (for the warning text) and the
// grantable cores and memory. ok is false when no daemon is reachable, so the
// caller leaves the charge for the daemon to cap at submit.
func (la *LocalAdmission) idleGrantableHost(ctx context.Context) (machineCores, grantableCores float64, grantableMemoryBytes int64, ok bool) {
	qs, err := wingdclient.Query(ctx, la.clientOptions())
	if err != nil {
		return 0, 0, 0, false
	}
	for _, r := range qs.Resources {
		switch r.Key {
		case "cores":
			machineCores = r.Capacity
			grantableCores = r.Capacity - r.Reserved
		case "memory":
			grantableMemoryBytes = int64(r.Capacity - r.Reserved)
		}
	}
	return machineCores, grantableCores, grantableMemoryBytes, machineCores > 0
}

// nodePin flattens a single node's explicit .Resources() declaration to a
// capacity.Pin, or nil when the node declared nothing.
func nodePin(node *sparkwing.JobNode) *capacity.Pin {
	if node == nil {
		return nil
	}
	if h := node.ResourceHints(); h != nil && (h.Cores > 0 || h.MemoryBytes > 0) {
		return &capacity.Pin{Cores: h.Cores, MemoryBytes: h.MemoryBytes}
	}
	return nil
}

// attachChildRun joins the parent's live lease (zero budget), then
// acquires any plan-level semaphores the parent lease does not already
// hold through a second, semaphores-only request.
func (la *LocalAdmission) attachChildRun(
	ctx context.Context,
	backends Backends,
	runID string,
	pipeline string,
	plan *sparkwing.Plan,
	onEvicted func(error),
) (*runLease, admitOutcome, error) {
	cl, err := wingdclient.EnsureDaemon(ctx, la.clientOptions())
	if err != nil {
		return nil, admitProceed, fmt.Errorf("local admission: %w", err)
	}
	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID:            runID,
		Pipeline:         pipeline,
		Repo:             currentRepoShortName(),
		PID:              os.Getpid(),
		ParentLeaseToken: la.ParentLeaseToken,
		Origin:           la.Origin,
	}, nil)
	if err != nil {
		cl.Close()
		return nil, admitProceed, fmt.Errorf("local admission: attach to parent lease: %w", err)
	}
	rl := &runLease{token: lease.Token, hostAdmitted: leaseCarriesHost(lease), leases: []*wingdclient.Lease{lease}}
	go lease.WatchControl(evictionHandler(runID, onEvicted), cancelHandler(onEvicted))

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
		SubLease:       true,
	})
	if err != nil || outcome != admitProceed {
		rl.release()
		return nil, outcome, err
	}
	rl.leases = append(rl.leases, extraLease)
	go extraLease.Watch(evictionHandler(runID, onEvicted))
	return rl, admitProceed, nil
}

func leaseCarriesHost(lease *wingdclient.Lease) bool {
	if lease == nil {
		return false
	}
	return lease.Resources.Cores > 0 || lease.Resources.MemoryBytes > 0
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
			fmt.Errorf("plan concurrency group %q: queued %s without a slot under OnLimit:Queue; run `sparkwing queue` to see who holds it", key, timeout))
		defer cancel()
	}
	cl, err := wingdclient.EnsureDaemon(acquireCtx, la.clientOptions())
	if err != nil {
		return nil, admitProceed, fmt.Errorf("local admission unreachable: could not reach the admission daemon: %w; run `sparkwing queue` to check the local admission state", err)
	}
	reporter := &queueWaitReporter{la: la, ctx: ctx, backends: backends, runID: runID}
	reporter.requestID = req.RunID
	stopHeartbeat := reporter.startHeartbeat(acquireCtx)
	lease, err := cl.Acquire(acquireCtx, req, reporter.onQueued)
	stopHeartbeat()
	if err != nil {
		cl.Close()
		if cause := context.Cause(acquireCtx); cause != nil && ctx.Err() == nil {
			appendPlanEvent(ctx, backends, runID, "admission_queue_timeout", nil)
			return nil, admitProceed, cause
		}
		var cancelErr *wingdclient.CancelledError
		if errors.As(err, &cancelErr) {
			appendPlanEvent(ctx, backends, runID, "admission_cancelled", nil)
			reason := cancelErr.Reason
			if reason == "" {
				reason = "cancelled via the admission daemon"
			}
			return nil, admitProceed, &runDaemonCanceledError{reason: reason}
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
	if reporter.waited() {
		appendPlanEvent(ctx, backends, runID, "admission_granted", nil)
		fmt.Fprintf(la.stderr(), "admitted; starting run\n")
	}
	return lease, admitProceed, nil
}

// queueWaitReporter renders a run's admission wait: a fresh line on each
// daemon position push, plus a heartbeat re-emit of the last-known
// position on an interval so a long silent wait never reads as a hang.
type queueWaitReporter struct {
	la        *LocalAdmission
	ctx       context.Context
	backends  Backends
	runID     string
	requestID string

	mu     sync.Mutex
	latest wingwire.Queued
	seen   bool
	since  time.Time
}

// onQueued handles a daemon position push: it records the latest state
// (starting the wait clock on the first push) and emits the full line.
func (r *queueWaitReporter) onQueued(q wingwire.Queued) {
	r.mu.Lock()
	if !r.seen {
		r.seen = true
		r.since = time.Now()
	}
	r.latest = q
	r.mu.Unlock()
	r.la.reportQueued(r.ctx, r.backends, r.runID, r.requestID, q)
}

// waited reports whether the run was ever queued, gating the terminal
// "admitted; starting run" line to runs that actually waited.
func (r *queueWaitReporter) waited() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen
}

// startHeartbeat re-emits the last-known queue position every
// heartbeat interval until ctx ends or the returned stop is called. It
// stays silent until the first daemon push has been seen.
func (r *queueWaitReporter) startHeartbeat(ctx context.Context) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(r.la.heartbeatInterval())
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				r.emitHeartbeat()
			}
		}
	}()
	return func() { close(done) }
}

func (r *queueWaitReporter) emitHeartbeat() {
	r.mu.Lock()
	if !r.seen {
		r.mu.Unlock()
		return
	}
	q := r.latest
	waited := time.Since(r.since)
	r.mu.Unlock()
	r.la.reportStillQueued(q, waited)
}

// reportQueued renders one queue-position update: a single stderr line
// plus an admission_wait event on the run row.
func (la *LocalAdmission) reportQueued(ctx context.Context, backends Backends, runID, requestID string, q wingwire.Queued) {
	ahead, noun, reason := queuePositionParts(q)
	fmt.Fprintf(la.stderr(),
		"queued for local admission: position %d of %d (%d %s ahead)%s; run `sparkwing queue` to see the full queue\n",
		q.Position, q.QueueLength, ahead, noun, reason)
	payload := fmt.Appendf(nil, `{"position":%d,"queue_length":%d,"request_id":%q}`, q.Position, q.QueueLength, requestID)
	appendPlanEvent(ctx, backends, runID, "admission_wait", payload)
}

// reportStillQueued re-emits the last-known position as a heartbeat,
// naming how long the run has waited so a stalled-looking queue reads as
// healthy backpressure. Stderr only -- no run-row event, to avoid
// flooding the row with duplicate waits.
func (la *LocalAdmission) reportStillQueued(q wingwire.Queued, waited time.Duration) {
	ahead, noun, reason := queuePositionParts(q)
	fmt.Fprintf(la.stderr(),
		"still queued for local admission after %s: position %d of %d (%d %s ahead)%s; run `sparkwing queue` to see the full queue\n",
		waited.Round(time.Second), q.Position, q.QueueLength, ahead, noun, reason)
}

// queuePositionParts derives the shared pieces of a queue-position line:
// the count of runs ahead, its singular/plural noun, and the "; reason"
// suffix naming what the run is blocked on.
func queuePositionParts(q wingwire.Queued) (ahead int, noun, reason string) {
	ahead = q.Position - 1
	if ahead < 0 {
		ahead = 0
	}
	noun = "runs"
	if ahead == 1 {
		noun = "run"
	}
	if q.BlockingReason != "" {
		reason = "; " + q.BlockingReason
	}
	return ahead, noun, reason
}

// admissionFailure maps a terminal fail-policy answer to a named error.
func admissionFailure(admErr *wingdclient.AdmissionError) error {
	switch admErr.Key {
	case "never_admissible":
		return errors.New("local admission: a concurrency group's cost exceeds its own capacity; lower the cost or raise the group's limit")
	case "duplicate", "invalid", "parent", "reattach":
		return fmt.Errorf("local admission: %w", admErr)
	default:
		return fmt.Errorf("plan concurrency group %q: slot full under OnLimit:Fail; run `sparkwing queue` to see who holds it", admErr.Key)
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

// cancelHandler adapts a daemon operator-cancel push into the
// run-cancelling error, so `sparkwing runs cancel` winds the run down on
// the same context-cancel path an interrupt uses.
func cancelHandler(onEvicted func(error)) func(wingwire.Cancel) {
	return func(c wingwire.Cancel) {
		if onEvicted == nil {
			return
		}
		reason := c.Reason
		if reason == "" {
			reason = "cancelled via the admission daemon"
		}
		onEvicted(&runDaemonCanceledError{reason: reason})
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

// resolveHostCost resolves a run's host charge and its provenance:
// an explicit .Resources() pin wins, else the measured profile once it
// has enough samples, else the conservative cold-start default. It also
// returns a drift warning when a pin has diverged far from measurement.
// A missing local store (cluster and remote paths) simply means no
// measured profile, so the pin-or-default order still holds. The final
// string is the loud over-capacity warning when an explicit pin exceeds
// this machine's grantable ceiling and was capped to run alone.
func (la *LocalAdmission) resolveHostCost(ctx context.Context, backends Backends, pipeline string, plan *sparkwing.Plan) (capacity.Resolution, *store.PipelineProfile, *capacity.Drift, string) {
	pin := planPin(plan)
	var profile *store.PipelineProfile
	if st := canonicalLocalStore(backends.State); st != nil && pipeline != "" {
		if p, err := st.GetPipelineProfile(ctx, pipeline, ""); err == nil {
			profile = p
		}
	}
	res := capacity.Resolve(pin, profile, runtime.NumCPU(), planTopologyHash(plan.Nodes()))
	res, overCap := la.applyHostCeiling(ctx, res)
	return res, profile, capacity.CheckDrift(pin, profile), overCap
}

// planPin flattens the run's explicit .Resources() declaration to a
// capacity.Pin: the plan-level hint when declared, else the largest
// node-level hint, else nil for a pipeline that declared nothing.
func planPin(plan *sparkwing.Plan) *capacity.Pin {
	if rh := plan.ResourceHints(); rh != nil && (rh.Cores > 0 || rh.MemoryBytes > 0) {
		return &capacity.Pin{Cores: rh.Cores, MemoryBytes: rh.MemoryBytes}
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
		return &capacity.Pin{Cores: cores, MemoryBytes: mem}
	}
	return nil
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
			Name:            key,
			Cost:            membership.Cost,
			Capacity:        limit.Capacity,
			Policy:          wingwire.Policy(limit.OnLimit),
			QueueTimeoutMS:  limit.QueueTimeout.Milliseconds(),
			CancelTimeoutMS: limit.CancelTimeout.Milliseconds(),
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
	la           *LocalAdmission
	token        string
	childToken   string
	hostAdmitted bool
}

func withLocalAdmission(
	ctx context.Context,
	la *LocalAdmission,
	leaseToken string,
	childToken string,
	hostAdmitted bool,
) context.Context {
	if la == nil {
		return ctx
	}
	if childToken == "" {
		childToken = leaseToken
	}
	ctx = context.WithValue(ctx, localAdmissionCtxKey{}, localAdmissionState{
		la:           la,
		token:        leaseToken,
		childToken:   childToken,
		hostAdmitted: hostAdmitted,
	})
	if leaseToken != "" {
		env := map[string]string{wingwire.LeaseTokenEnv: leaseToken}
		if childToken != "" {
			env[wingwire.ChildLeaseTokenEnv] = childToken
		}
		ctx = sparkwing.WithCommandEnv(ctx, env)
	}
	return ctx
}

func localAdmissionFromContext(ctx context.Context) (*LocalAdmission, string, bool) {
	state, ok := ctx.Value(localAdmissionCtxKey{}).(localAdmissionState)
	if !ok {
		return nil, "", false
	}
	return state.la, state.token, state.hostAdmitted
}

// leaseTriggerEnv is the env a parent stamps onto spawned child
// triggers so the child attaches to the parent's lease. Nil when the
// run holds no daemon lease.
func leaseTriggerEnv(ctx context.Context) map[string]string {
	state, ok := ctx.Value(localAdmissionCtxKey{}).(localAdmissionState)
	if !ok || state.childToken == "" {
		return nil
	}
	token := state.childToken
	if token == "" {
		return nil
	}
	return map[string]string{wingwire.LeaseTokenEnv: token}
}

func childAttachTokenFromEnv(env map[string]string) string {
	if env == nil {
		return ""
	}
	if token := env[wingwire.ChildLeaseTokenEnv]; token != "" {
		return token
	}
	return env[wingwire.LeaseTokenEnv]
}

func childAttachTokenFromProcessEnv() string {
	if token := os.Getenv(wingwire.ChildLeaseTokenEnv); token != "" {
		return token
	}
	return os.Getenv(wingwire.LeaseTokenEnv)
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
		RunID:          nodeSemaphoreRunID(runID, nodeID),
		SemaphoresOnly: true,
		Semaphores:     []wingwire.SemaphoreClaim{claim},
		SubLease:       true,
	}, onQueued)
	if err != nil {
		cl.Close()
		return nil, err
	}
	return lease, nil
}

func nodeSemaphoreRunID(runID, nodeID string) string {
	return runID + "/node-semaphore/" + encodeNodeAdmissionID(nodeID)
}

func nodeHostRunID(runID, nodeID string) string {
	return runID + "/node-host/" + encodeNodeAdmissionID(nodeID)
}

func encodeNodeAdmissionID(nodeID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(nodeID))
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
