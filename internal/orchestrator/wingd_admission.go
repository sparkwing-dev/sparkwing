package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	ParentRunID      string
	// ParentNodeID, with ParentRunID, attaches a hosted run to what that node
	// of its parent run holds.
	ParentNodeID string

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

func fmtAdmissionDur(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

const localPlanSemsID = "/plan"

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

func leaseCarriesHost(lease *wingdclient.Lease) bool {
	if lease == nil {
		return false
	}
	return lease.Resources.Cores > 0 || lease.Resources.MemoryBytes > 0
}

func (la *LocalAdmission) reportGranted(ctx context.Context, backends Backends, runID, participant, requestID, displayID string) {
	appendAdmissionEvent(ctx, backends, runID, participant, "admission_granted", admissionRequestPayload(requestID))
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

const terminalCheckKey = "terminal-check"

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
