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
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type LocalAdmission struct {
	Home string

	Version string

	ParentLeaseToken string

	Origin wingwire.Origin

	AdmissionClass sparkwing.AdmissionClass

	Out io.Writer

	Delegate sparkwing.Logger

	Spawn func(home, version string) error

	PipelineClient bool

	Logf func(format string, args ...any)

	DialTimeout time.Duration
	Backoff     time.Duration

	loggedMu sync.Mutex
	logged   map[string]bool

	QueueHeartbeat time.Duration

	reservedNodeLeaseToken string
}

// NewReservedNodeAdmission attaches node execution to capacity that a caller
// already reserved. The caller retains ownership of the lease and must release
// it after execution stops.
func NewReservedNodeAdmission(home, version, leaseToken string, origin wingwire.Origin) *LocalAdmission {
	return &LocalAdmission{
		Home:                   home,
		Version:                version,
		Origin:                 origin,
		reservedNodeLeaseToken: leaseToken,
	}
}

func (la *LocalAdmission) attachReservedNode(ctx context.Context, priority int) (context.Context, bool) {
	if la == nil || la.reservedNodeLeaseToken == "" {
		return ctx, false
	}
	token := la.reservedNodeLeaseToken
	return withLocalAdmission(ctx, la, token, token, true, priority, runCharge{}), true
}

const defaultQueueHeartbeat = 30 * time.Second

func (la *LocalAdmission) heartbeatInterval() time.Duration {
	if la.QueueHeartbeat > 0 {
		return la.QueueHeartbeat
	}
	return defaultQueueHeartbeat
}

func (la *LocalAdmission) clientOptions() wingdclient.Options {
	spawn := la.Spawn
	if la.PipelineClient && spawn == nil {
		// safety: PipelineClient and a non-self-exec Spawn are two halves of
		// one stance, and a caller that sets the first without the second
		// must not fall through to a default that re-execs this binary as
		// the daemon. Resolve it the way pipelineAdmission would.
		if resolved, ok := wingdclient.HostSpawn(); ok {
			spawn = resolved
		} else {
			spawn = wingdclient.NoHostSpawn
		}
	}
	return wingdclient.Options{
		Home:        la.Home,
		Version:     la.Version,
		Spawn:       spawn,
		NoTakeover:  la.PipelineClient,
		DialTimeout: la.DialTimeout,
		Backoff:     la.Backoff,
		Logf:        la.logf,
	}
}

func (la *LocalAdmission) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	la.loggedMu.Lock()
	if la.logged == nil {
		la.logged = map[string]bool{}
	}
	seen := la.logged[msg]
	la.logged[msg] = true
	la.loggedMu.Unlock()
	if seen {
		return
	}
	if la.Logf != nil {
		la.Logf("%s", msg)
		return
	}
	fmt.Fprintln(os.Stderr, "local admission: "+msg)
}

func (la *LocalAdmission) out() io.Writer {
	if la.Out != nil {
		return la.Out
	}
	return os.Stdout
}

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

func fmtAdmissionDur(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

const localPlanSemsID = "/plan"

type runLease struct {
	token        string
	childToken   string
	hostAdmitted bool
	leases       []*wingdclient.Lease

	driftWarning string

	charge runCharge
}

func (rl *runLease) release() {
	if rl == nil {
		return
	}
	for _, l := range rl.leases {
		_ = l.Release()
	}
}

type admitOutcome string

const (
	admitProceed admitOutcome = ""
	admitSkipped admitOutcome = "skip"
)

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
	class := effectiveAdmissionClass(plan, la.AdmissionClass)
	la.AdmissionClass = class
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
		Priority:           plan.PriorityValue(),
		Class:              string(class),
	}
	if prof != nil {
		req.ExpectedP99MS = prof.P99Duration.Milliseconds()
		req.SampleCount = prof.SampleCount
	}
	warning := hostChargeWarning(drift, overCap)
	if warning != "" {
		req.DriftWarning = warning
		fmt.Fprintln(la.out(), warning)
	}
	if note := measuringNarration(res, prof); note != "" {
		fmt.Fprintln(la.out(), note)
	}
	submitted := time.Now()
	lease, outcome, err := la.acquireBlocking(ctx, backends, runID, req)
	if err != nil || outcome != admitProceed {
		return nil, outcome, err
	}
	if backends.LocalCoordination && pipeline != "" {
		_ = backends.State.RecordWaitObservation(ctx, currentProfileKey(pipeline), time.Since(submitted))
	}
	rl := &runLease{token: lease.Token, hostAdmitted: hostPinned, leases: []*wingdclient.Lease{lease}}
	if hostPinned || len(req.Semaphores) > 0 {
		rl.childToken = lease.Token
	}
	rl.driftWarning = warning
	rl.charge = runCharge{Cores: res.Cores, MemoryBytes: res.MemoryBytes}
	if lease.SoleRunUnderLoad {
		fmt.Fprintf(la.out(),
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

func measuringNarration(res capacity.Resolution, prof *store.PipelineProfile) string {
	switch res.Source {
	case store.CostSourceMeasuring:
		return fmt.Sprintf("re-measuring at %.1f cores (prior charge); runs under contention only raise the floor, clean runs finalize the price",
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

func hostChargeWarning(drift *capacity.Drift, overCapacity string) string {
	if overCapacity != "" {
		return overCapacity
	}
	if drift != nil {
		return drift.Message
	}
	return ""
}

func (la *LocalAdmission) admitNode(
	ctx context.Context,
	backends Backends,
	pipeline, runID, nodeID string,
	node *sparkwing.JobNode,
	priority int,
) (*runLease, error) {
	res, prof, _, overCap := la.resolveNodeHostCost(ctx, backends, pipeline, nodeID, node)
	req := wingwire.AdmissionRequest{
		RunID:              nodeHostRunID(runID, nodeID),
		OwnerRunID:         runID,
		OwnerLeaseToken:    localAdmissionLeaseTokenFromContext(ctx),
		DisplayRunID:       nodeDisplayRunID(runID, nodeID),
		Pipeline:           pipeline,
		Repo:               currentRepoShortName(),
		PID:                os.Getpid(),
		Resources:          wingwire.HostResources{Cores: res.Cores, MemoryBytes: res.MemoryBytes},
		CostSource:         wingwire.CostSource(res.Source),
		ExpectedDurationMS: res.ExpectedDuration.Milliseconds(),
		Origin:             la.Origin,
		DriftWarning:       overCap,
		SubLease:           true,
		Priority:           priority,
		Class:              string(la.AdmissionClass),
	}
	if prof != nil {
		req.ExpectedP99MS = prof.P99Duration.Milliseconds()
		req.SampleCount = prof.SampleCount
	}
	lease, outcome, err := la.acquireBlocking(ctx, backends, runID, req)
	if err != nil || outcome != admitProceed {
		return nil, err
	}
	rl := &runLease{
		token:        lease.Token,
		childToken:   lease.Token,
		hostAdmitted: leaseCarriesHost(lease),
		leases:       []*wingdclient.Lease{lease},
		charge:       runCharge{Cores: res.Cores, MemoryBytes: res.MemoryBytes},
	}
	return rl, nil
}

func (la *LocalAdmission) resolveNodeHostCost(ctx context.Context, backends Backends, pipeline, nodeID string, node *sparkwing.JobNode) (capacity.Resolution, *store.PipelineProfile, *capacity.Drift, string) {
	pin := nodePin(node)
	key := currentProfileKey(pipeline)
	var profile *store.PipelineProfile
	if backends.LocalCoordination && pipeline != "" {
		if p, err := backends.State.GetPipelineProfile(ctx, key, nodeID); err == nil {
			profile = p
		}
	}
	res := capacity.Resolve(pin, profile, runtime.NumCPU(), "")
	res, overCap := la.applyHostCeiling(ctx, res, key)
	return res, profile, capacity.CheckDrift(pin, profile), overCap
}

func (la *LocalAdmission) applyHostCeiling(ctx context.Context, res capacity.Resolution, profileKey string) (capacity.Resolution, string) {
	machineCores, grantCores, grantMem, ok := la.idleGrantableHost(ctx)
	if !ok {
		return res, ""
	}
	return capacity.ApplyHostCeiling(res, store.DisplayProfileKey(profileKey), machineCores, grantCores, grantMem)
}

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

func nodePin(node *sparkwing.JobNode) *capacity.Pin {
	if node == nil {
		return nil
	}
	if h := node.ResourceHints(); h != nil && (h.Cores > 0 || h.MemoryBytes > 0) {
		return &capacity.Pin{Cores: h.Cores, MemoryBytes: h.MemoryBytes}
	}
	return nil
}

func (la *LocalAdmission) attachChildRun(
	ctx context.Context,
	backends Backends,
	runID string,
	pipeline string,
	plan *sparkwing.Plan,
	onEvicted func(error),
) (*runLease, admitOutcome, error) {
	cl, err := la.ensureDaemon(ctx)
	if err != nil {
		return nil, admitProceed, fmt.Errorf("local admission: %w", err)
	}
	// safety: server grants or rejects a ParentLeaseToken immediately; child attach is never queued, so nil is safe.
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
		RunID:           runID + localPlanSemsID,
		OwnerRunID:      runID,
		OwnerLeaseToken: lease.Token,
		DisplayRunID:    runID,
		SemaphoresOnly:  true,
		Semaphores:      extra,
		SubLease:        true,
		Priority:        plan.PriorityValue(),
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

func (la *LocalAdmission) acquireBlocking(
	ctx context.Context,
	backends Backends,
	runID string,
	req wingwire.AdmissionRequest,
) (*wingdclient.Lease, admitOutcome, error) {
	waits := admissionWaitTrackerFromContext(ctx)
	participant := admissionWaitParticipantFromContext(ctx)
	waits.begin(participant)
	defer waits.end(participant)
	acquireCtx := ctx
	if key, timeout := tightestQueueTimeout(req.Semaphores); timeout > 0 {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeoutCause(ctx, timeout,
			fmt.Errorf("plan concurrency group %q: queued %s without a slot under OnLimit:Queue; run `sparkwing queue` to see who holds it", key, timeout))
		defer cancel()
	}
	cl, err := la.ensureDaemon(acquireCtx)
	if err != nil {
		// safety: an answered takeover exhaustion is a version conflict, not an
		// unreachable daemon; preserve the actionable diagnosis.
		if errors.Is(err, wingdclient.ErrTakeoverExhausted) {
			return nil, admitProceed, fmt.Errorf("local admission refused a version conflict: %w", err)
		}

		if errors.Is(err, ErrDaemonStoreSchemaTooOld) ||
			errors.Is(err, wingdclient.ErrDaemonTooOld) ||
			errors.Is(err, wingdclient.ErrProtocolTooOld) ||
			errors.Is(err, wingdclient.ErrNoDaemonHost) ||
			errors.Is(err, wingdclient.ErrDaemonHostUnusable) ||
			errors.Is(err, wingdclient.ErrDaemonHostFailed) {
			return nil, admitProceed, fmt.Errorf("local admission: %w", err)
		}
		return nil, admitProceed, fmt.Errorf("local admission unreachable: could not reach the admission daemon: %w; run `sparkwing queue` to check the local admission state", err)
	}
	displayID := req.DisplayRunID
	if displayID == "" {
		displayID = req.RunID
	}
	if displayID == "" {
		displayID = runID
	}
	reporter := &queueWaitReporter{
		la: la, backends: backends, runID: runID, nodeID: participant,
		displayID: displayID, requestID: req.RunID, request: req,
		snapshot: func(queryCtx context.Context) (wingwire.QueueState, error) {
			return wingdclient.Query(queryCtx, la.clientOptions())
		},
	}
	stopHeartbeat := reporter.startHeartbeat(acquireCtx)
	lease, err := cl.Acquire(acquireCtx, req, reporter.onQueued)
	stopHeartbeat()
	if err != nil {
		cl.Close()
		if cause := context.Cause(acquireCtx); cause != nil && ctx.Err() == nil {
			appendAdmissionEvent(ctx, backends, runID, participant, "admission_queue_timeout", admissionRequestPayload(req.RunID))
			return nil, admitProceed, cause
		}
		var cancelErr *wingdclient.CancelledError
		if errors.As(err, &cancelErr) {
			appendAdmissionEvent(ctx, backends, runID, participant, "admission_cancelled", admissionRequestPayload(req.RunID))
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
				return nil, admitProceed, admissionFailure(req.Semaphores, admErr)
			}
			return nil, admitProceed, fmt.Errorf("local admission: %w", admErr)
		}
		return nil, admitProceed, fmt.Errorf("local admission: %w", err)
	}
	if reporter.waited() {
		appendAdmissionEvent(ctx, backends, runID, participant, "admission_granted", admissionRequestPayload(req.RunID))
		if la.Delegate != nil {
			la.Delegate.Emit(sparkwing.LogRecord{
				TS:    time.Now(),
				Level: "info",
				Event: "admission_granted",
				Msg:   "admitted; starting run",
			})
		}
		fmt.Fprintf(la.out(), "admitted; starting run: participant %s\n", displayID)
	}
	return lease, admitProceed, nil
}

type queueWaitReporter struct {
	la        *LocalAdmission
	backends  Backends
	runID     string
	nodeID    string
	requestID string
	displayID string
	request   wingwire.AdmissionRequest
	snapshot  func(context.Context) (wingwire.QueueState, error)

	mu      sync.Mutex
	latest  wingwire.Queued
	seen    bool
	since   time.Time
	changed chan struct{}

	reported      bool
	lastSignature string
}

func (r *queueWaitReporter) onQueued(q wingwire.Queued) {
	r.mu.Lock()
	first := !r.seen
	if first {
		r.seen = true
		r.since = time.Now()
	}
	r.latest = q
	r.mu.Unlock()
	if first {
		select {
		case r.changed <- struct{}{}:
		default:
		}
	}
}

func (r *queueWaitReporter) waited() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen
}

func (r *queueWaitReporter) startHeartbeat(ctx context.Context) (stop func()) {
	r.changed = make(chan struct{}, 1)
	reportCtx, cancel := context.WithCancel(ctx)
	var finished sync.WaitGroup
	finished.Add(1)
	go func() {
		defer finished.Done()
		var timer *time.Timer
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		var ticks <-chan time.Time
		reportIndex := 0
		for {
			select {
			case <-reportCtx.Done():
				return
			case <-r.changed:
				r.emitUpdate(reportCtx)
				if timer == nil {
					timer = time.NewTimer(queueWaitReportDelay(r.la.heartbeatInterval(), reportIndex))
					ticks = timer.C
				}
			case <-ticks:
				r.emitUpdate(reportCtx)
				reportIndex++
				timer.Reset(queueWaitReportDelay(r.la.heartbeatInterval(), reportIndex))
			}
		}
	}()
	return func() {
		cancel()
		finished.Wait()
	}
}

func queueWaitReportDelay(base time.Duration, reportIndex int) time.Duration {
	multipliers := [...]int{1, 1, 2, 6, 10}
	if reportIndex >= len(multipliers) {
		return 10 * base
	}
	return time.Duration(multipliers[reportIndex]) * base
}

const queueSnapshotTimeout = 2 * time.Second

func (r *queueWaitReporter) emitUpdate(ctx context.Context) {
	r.mu.Lock()
	if !r.seen {
		r.mu.Unlock()
		return
	}
	q := r.latest
	waited := time.Since(r.since)
	r.mu.Unlock()

	queryCtx, cancel := context.WithTimeout(ctx, queueSnapshotTimeout)
	qs, err := r.snapshot(queryCtx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	available := err == nil
	if available {
		waiter, ok := queueWaitCurrent(r.requestID, qs)
		if !ok {
			return
		}
		q.Position = waiter.Position
		q.QueueLength = len(qs.Waiters)
		q.BlockingReason = waiter.BlockingReason
		if len(waiter.WaitingOn) > 0 {
			q.Key = waiter.WaitingOn[0]
		}
	}
	signature := queueWaitMaterialSignature(r.requestID, q, qs, available)
	if r.reported && signature == r.lastSignature {
		return
	}
	r.reported = true
	r.lastSignature = signature
	msg := formatQueueWait(r.request, r.requestID, r.displayID, q, qs, available, waited)
	r.la.reportQueued(ctx, r.backends, r.runID, r.nodeID, r.requestID, r.displayID, q, msg)
}

func (la *LocalAdmission) reportQueued(ctx context.Context, backends Backends, runID, nodeID, requestID, displayID string, q wingwire.Queued, msg string) {
	fmt.Fprintln(la.out(), msg)
	payload := fmt.Appendf(nil, `{"position":%d,"queue_length":%d,"request_id":%q,"display_id":%q}`, q.Position, q.QueueLength, requestID, displayID)
	appendAdmissionEvent(ctx, backends, runID, nodeID, "admission_wait", payload)
	if la.Delegate != nil {
		la.Delegate.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "info",
			Event: "admission_wait",
			Msg:   msg,
			Attrs: map[string]any{
				"position":     q.Position,
				"queue_length": q.QueueLength,
				"request_id":   requestID,
				"display_id":   displayID,
			},
		})
	}
}

func admissionRequestPayload(requestID string) []byte {
	return fmt.Appendf(nil, `{"request_id":%q}`, requestID)
}

func appendAdmissionEvent(ctx context.Context, backends Backends, runID, participantID, kind string, payload []byte) {
	if backends.State == nil {
		return
	}
	noteEvent(ctx, backends.State, runID, participantID, kind, payload)
}

func queueWaitMaterialSignature(requestID string, q wingwire.Queued, qs wingwire.QueueState, available bool) string {
	parts := []string{fmt.Sprintf("state=%t", available), fmt.Sprintf("position=%d/%d", q.Position, q.QueueLength)}
	if !available {
		return strings.Join(append(parts, "blocking="+q.Key), "|")
	}
	for _, h := range opsview.RunningHolders(qs) {
		identity := h.ParticipantID
		if identity == "" {
			identity = h.RunID
		}
		parts = append(parts, fmt.Sprintf("holder=%s:%s:%g:%d", identity, queueWaitHolderName(h), h.Resources.Cores, h.Resources.MemoryBytes))
	}
	sort.Strings(parts[2:])
	waitingOn := queueWaitBlockingDimensions(requestID, q, qs)
	clearKnown := qs.ExpectedClearMS != nil && *qs.ExpectedClearMS > 0
	parts = append(parts, "blocking="+strings.Join(waitingOn, ","), fmt.Sprintf("clear=%t", clearKnown))
	return strings.Join(parts, "|")
}

func formatQueueWait(req wingwire.AdmissionRequest, requestID, displayID string, q wingwire.Queued, qs wingwire.QueueState, available bool, waited time.Duration) string {
	place := "you are next"
	if q.Position > 1 {
		place = fmt.Sprintf("position %d of %d", q.Position, q.QueueLength)
	}
	if !available {
		return fmt.Sprintf("admission: machine state unavailable, %d queued; %s -- %s; participant %s; run `sparkwing queue` to inspect the current queue",
			q.QueueLength, place, queueWaitNeed(req), displayID)
	}
	holders := opsview.RunningHolders(qs)
	running := fmt.Sprintf("%d running", len(holders))
	if len(holders) > 0 {
		items := make([]string, 0, len(holders))
		for _, h := range holders {
			charge := formatQueueHostResources(h.Resources)
			if h.Parent != "" {
				charge = "attached"
			}
			items = append(items, queueWaitHolderName(h)+" "+charge)
		}
		running += " (" + strings.Join(items, ", ") + ")"
	}
	pieces := []string{fmt.Sprintf("admission: %s, %d queued; %s -- %s", running, len(qs.Waiters), place, queueWaitNeed(req))}
	if resources := queueWaitResourceState(qs, queueWaitBlockingDimensions(requestID, q, qs)); resources != "" {
		pieces = append(pieces, resources)
	}
	if qs.ExpectedClearMS != nil && *qs.ExpectedClearMS > 0 {
		pieces = append(pieces, "expected clear ~"+fmtAdmissionDur(*qs.ExpectedClearMS))
	}
	if waited >= time.Second {
		pieces = append(pieces, "waited "+waited.Round(time.Second).String())
	}
	return strings.Join(pieces, "; ")
}

func queueWaitHolderName(h wingwire.Holder) string {
	if h.Pipeline != "" {
		return h.Pipeline
	}
	if h.DisplayRunID != "" {
		return h.DisplayRunID
	}
	return h.RunID
}

func queueWaitNeed(req wingwire.AdmissionRequest) string {
	resources := formatQueueHostResources(req.Resources)
	if resources == "no host resources" && len(req.Semaphores) > 0 {
		names := make([]string, 0, len(req.Semaphores))
		for _, claim := range req.Semaphores {
			names = append(names, claim.Name)
		}
		sort.Strings(names)
		resources = strings.Join(names, "+") + " slot"
	}
	need := "needs " + resources
	if req.CostSource != "" {
		source := string(req.CostSource)
		if req.CostSource == wingwire.CostSourcePin {
			source = "pinned"
		}
		need += " (" + source + ")"
	}
	return need
}

func formatQueueHostResources(resources wingwire.HostResources) string {
	parts := make([]string, 0, 2)
	if resources.Cores > 0 {
		parts = append(parts, fmt.Sprintf("%.1f cores", resources.Cores))
	}
	if resources.MemoryBytes > 0 {
		parts = append(parts, formatQueueBytes(resources.MemoryBytes))
	}
	if len(parts) == 0 {
		return "no host resources"
	}
	return strings.Join(parts, "/")
}

func formatQueueBytes(bytes int64) string {
	const gib = int64(1024 * 1024 * 1024)
	const mib = int64(1024 * 1024)
	if bytes >= gib {
		return fmt.Sprintf("%.1f GiB", float64(bytes)/float64(gib))
	}
	return fmt.Sprintf("%.1f MiB", float64(bytes)/float64(mib))
}

func queueWaitResourceState(qs wingwire.QueueState, waitingOn []string) string {
	key := "cores"
	for _, dimension := range waitingOn {
		if dimension == "cores" || dimension == "memory" {
			key = dimension
			break
		}
	}
	for _, resource := range qs.Resources {
		if resource.Key != key {
			continue
		}
		amount := func(value float64) string { return fmt.Sprintf("%.1f", value) }
		if key == "memory" {
			amount = func(value float64) string { return formatQueueBytes(int64(value)) }
		}
		external := "external " + opsview.ExternalAmount(resource)
		return fmt.Sprintf("%s free, %s held, %s", amount(opsview.ResourceAvailable(resource)), amount(resource.Held), external)
	}
	return ""
}

func queueWaitBlockingDimensions(requestID string, q wingwire.Queued, qs wingwire.QueueState) []string {
	for _, waiter := range qs.Waiters {
		if waiter.ParticipantID == requestID || (waiter.ParticipantID == "" && waiter.RunID == requestID) {
			out := append([]string(nil), waiter.WaitingOn...)
			sort.Strings(out)
			return out
		}
	}
	if q.Key != "" {
		return []string{q.Key}
	}
	return nil
}

func queueWaitCurrent(requestID string, qs wingwire.QueueState) (wingwire.Waiter, bool) {
	for _, waiter := range qs.Waiters {
		if waiter.ParticipantID == requestID || (waiter.ParticipantID == "" && waiter.RunID == requestID) {
			return waiter, true
		}
	}
	return wingwire.Waiter{}, false
}

// safety: only a key this request claimed can mean exhausted capacity. Every
// other key is the daemon's own, and calling one a full slot sends the operator
// to `sparkwing queue` after a holder that does not exist.
func admissionFailure(claims []wingwire.SemaphoreClaim, admErr *wingdclient.AdmissionError) error {
	if requestClaimsKey(claims, admErr.Key) {
		return fmt.Errorf("plan concurrency group %q: slot full under OnLimit:Fail; run `sparkwing queue` to see who holds it", admErr.Key)
	}
	return daemonRefusal(admErr)
}

// safety: the node path claims exactly one key, and the daemon evicts under
// PolicyFail for reasons of its own too, so only that key can mean a full slot.
func nodeAdmissionFailure(claim wingwire.SemaphoreClaim, admErr *wingdclient.AdmissionError) error {
	if requestClaimsKey([]wingwire.SemaphoreClaim{claim}, admErr.Key) {
		return fmt.Errorf("concurrency key %q slot full under OnLimit:Fail", claim.Name)
	}
	return daemonRefusal(admErr)
}

const terminalCheckKey = "terminal-check"

func daemonRefusal(admErr *wingdclient.AdmissionError) error {
	switch admErr.Key {
	case "never_admissible":
		if admErr.Reason != "" {
			return fmt.Errorf("local admission: this request can never be admitted on this box: %s", admErr.Reason)
		}
		return errors.New("local admission: a concurrency group's cost exceeds its own capacity; lower the cost or raise the group's limit")
	case terminalCheckKey:
		return fmt.Errorf("local admission: %w; the daemon refused before any capacity decision, so compare its runs-store schema with the store's. %s",
			admErr, daemonUpgradeRemedy(store.ExpectedSchemaVersion()))
	default:
		return fmt.Errorf("local admission: %w", admErr)
	}
}

func requestClaimsKey(claims []wingwire.SemaphoreClaim, key string) bool {
	for _, claim := range claims {
		if claim.Name == key {
			return true
		}
	}
	return false
}

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

func (la *LocalAdmission) resolveHostCost(ctx context.Context, backends Backends, pipeline string, plan *sparkwing.Plan) (capacity.Resolution, *store.PipelineProfile, *capacity.Drift, string) {
	pin := planPin(plan)
	key := currentProfileKey(pipeline)
	var profile *store.PipelineProfile
	if backends.LocalCoordination && pipeline != "" {
		if p, err := backends.State.GetPipelineProfile(ctx, key, ""); err == nil {
			profile = p
		}
	}
	res := capacity.Resolve(pin, profile, runtime.NumCPU(), capacityFingerprint(plan))
	res, overCap := la.applyHostCeiling(ctx, res, key)
	return res, profile, capacity.CheckDrift(pin, profile), overCap
}

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

func groupUsesLocalDaemon(group *sparkwing.ConcurrencyGroup) bool {
	switch group.Limit().Scope {
	case sparkwing.ScopeBox, sparkwing.ScopeRun:
		return true
	default:
		return false
	}
}

type localAdmissionCtxKey struct{}

type localAdmissionState struct {
	la           *LocalAdmission
	token        string
	childToken   string
	hostAdmitted bool
	priority     int
}

func withLocalAdmission(
	ctx context.Context,
	la *LocalAdmission,
	leaseToken string,
	childToken string,
	hostAdmitted bool,
	priority int,
	charge runCharge,
) context.Context {
	if la == nil {
		return ctx
	}
	ctx = withAdmittedCharge(ctx, charge)
	ctx = context.WithValue(ctx, localAdmissionCtxKey{}, localAdmissionState{
		la:           la,
		token:        leaseToken,
		childToken:   childToken,
		hostAdmitted: hostAdmitted,
		priority:     priority,
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

func leaseTokensFromContext(ctx context.Context) (string, string) {
	state, ok := ctx.Value(localAdmissionCtxKey{}).(localAdmissionState)
	if !ok {
		return "", ""
	}
	return state.token, state.childToken
}

func localAdmissionChildTokenFromContext(ctx context.Context) string {
	state, ok := ctx.Value(localAdmissionCtxKey{}).(localAdmissionState)
	if !ok {
		return ""
	}
	return state.childToken
}

func localAdmissionLeaseTokenFromContext(ctx context.Context) string {
	state, ok := ctx.Value(localAdmissionCtxKey{}).(localAdmissionState)
	if !ok {
		return ""
	}
	return state.token
}

func localAdmissionPriorityFromContext(ctx context.Context) int {
	state, ok := ctx.Value(localAdmissionCtxKey{}).(localAdmissionState)
	if !ok {
		return 0
	}
	return state.priority
}

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

func (la *LocalAdmission) acquireNodeSlot(
	ctx context.Context,
	runID, nodeID string,
	claim wingwire.SemaphoreClaim,
	priority int,
	onQueued func(wingwire.Queued),
) (*wingdclient.Lease, error) {
	return la.acquireNodeAdmission(ctx, wingwire.AdmissionRequest{
		RunID:           nodeSemaphoreRunID(runID, nodeID),
		OwnerRunID:      runID,
		OwnerLeaseToken: localAdmissionLeaseTokenFromContext(ctx),
		DisplayRunID:    nodeDisplayRunID(runID, nodeID),
		SemaphoresOnly:  true,
		Semaphores:      []wingwire.SemaphoreClaim{claim},
		SubLease:        true,
		Priority:        priority,
	}, onQueued)
}

func (la *LocalAdmission) acquireNodeHostSlot(
	ctx context.Context,
	backends Backends,
	pipeline, runID, nodeID string,
	node *sparkwing.JobNode,
	claim wingwire.SemaphoreClaim,
	priority int,
	onQueued func(wingwire.Queued),
) (*wingdclient.Lease, error) {
	res, _, _, overCap := la.resolveNodeHostCost(ctx, backends, pipeline, nodeID, node)
	return la.acquireNodeAdmission(ctx, wingwire.AdmissionRequest{
		RunID:              nodeHostRunID(runID, nodeID),
		OwnerRunID:         runID,
		OwnerLeaseToken:    localAdmissionLeaseTokenFromContext(ctx),
		DisplayRunID:       nodeDisplayRunID(runID, nodeID),
		Pipeline:           pipeline,
		Repo:               currentRepoShortName(),
		PID:                os.Getpid(),
		Resources:          wingwire.HostResources{Cores: res.Cores, MemoryBytes: res.MemoryBytes},
		Semaphores:         []wingwire.SemaphoreClaim{claim},
		CostSource:         wingwire.CostSource(res.Source),
		ExpectedDurationMS: res.ExpectedDuration.Milliseconds(),
		Origin:             la.Origin,
		DriftWarning:       overCap,
		SubLease:           true,
		Priority:           priority,
	}, onQueued)
}

func (la *LocalAdmission) acquireNodeAdmission(
	ctx context.Context,
	req wingwire.AdmissionRequest,
	onQueued func(wingwire.Queued),
) (*wingdclient.Lease, error) {
	cl, err := la.ensureDaemon(ctx)
	if err != nil {
		return nil, fmt.Errorf("local admission: %w", err)
	}
	lease, err := cl.Acquire(ctx, req, onQueued)
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

func nodeDisplayRunID(runID, nodeID string) string {
	return runID + "/" + nodeID
}

func encodeNodeAdmissionID(nodeID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(nodeID))
}

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
