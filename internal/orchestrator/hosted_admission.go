package orchestrator

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/capacity"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// safety: a daemon unreachable for a reason a local run degrades on leaves the run unadmitted, as in the dispatcher.
func hostedAdmission(ctx context.Context, opts *Options, backends Backends, plan func() *nodeproto.PlanDoc) (*nodehost.Admission, error) {
	a := &nodehost.Admission{Slots: backends.Concurrency}
	trigger := opts.Trigger.Source
	if trigger == "" {
		trigger = "manual"
	}
	a.Class = string(inferredAdmissionClass(opts.AdmissionClass, trigger))
	if opts.prioritySet {
		a.Priority = &opts.priority.value
	}
	la := opts.Admission
	if la == nil {
		return a, nil
	}
	cl, err := la.ensureDaemon(ctx)
	if err != nil {
		if la.unhostedOutcome(err, opts.standalone.state(backends.APISocket != "")) {
			return a, nil
		}
		return nil, err
	}
	warnHostedClose("admission probe", cl.Close())
	if la.Delegate == nil {
		la.Delegate = opts.Delegate
	}
	if la.ParentLeaseToken != "" && la.ParentRunID == "" {
		la.ParentRunID = parentRunIDFromContext(ctx)
	}
	a.Daemon = &hostedAdmitter{la: la, backends: backends, pipeline: opts.Pipeline, plan: plan}
	return a, nil
}

// safety: PID stays 0 because the daemon's stall sampling reads that process,
// and this host process is not the one doing the node's work.
type hostedAdmitter struct {
	la       *LocalAdmission
	backends Backends
	pipeline string
	plan     func() *nodeproto.PlanDoc
	// safety: set by the run lease, which is granted before any node asks, so node requests read them unlocked.
	attachToken  string
	attachedHost bool
}

func (h *hostedAdmitter) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (nodehost.Lease, error) {
	switch {
	case h.la.ParentLeaseToken != "" && req.OwnerRunID == "":
		return h.attach(ctx, req)
	case h.attachedHost && !req.SemaphoresOnly:
		// safety: the parent's lease already holds the host for an attached run, so its nodes take none,
		// as the dispatcher's did, and a node's group still queues on its own semaphore.
		if len(req.Semaphores) == 0 {
			return &heldLease{token: h.attachToken, done: make(chan struct{})}, nil
		}
		req.SemaphoresOnly, req.Resources, req.Class = true, wingwire.HostResources{}, ""
	case !req.SemaphoresOnly && req.Resources == (wingwire.HostResources{}):
		h.profileCost(ctx, &req)
	case !req.SemaphoresOnly && req.CostSource == "":
		// safety: resources a request names come from a .Resources() pin, which the daemon refuses
		// rather than shrinks when the machine cannot hold it.
		req.CostSource = wingwire.CostSourcePin
	}
	req.Repo, req.Origin, req.PID = currentRepoShortName(), h.la.Origin, 0
	cl, err := h.la.ensureDaemon(ctx)
	if err != nil {
		return nil, err
	}
	runID, node, _ := strings.Cut(req.RunID, "/node-host/")
	if raw, err := base64.RawURLEncoding.DecodeString(node); err == nil {
		node = string(raw)
	}
	reporter := &queueWaitReporter{
		la: h.la, backends: h.backends, runID: runID, nodeID: node,
		displayID: cmp.Or(req.DisplayRunID, req.RunID), requestID: req.RunID, request: req,
		snapshot: func(queryCtx context.Context) (wingwire.QueueState, error) {
			return wingdclient.Query(queryCtx, h.la.clientOptions())
		},
	}
	stopNarration := reporter.startHeartbeat(ctx)
	lease, err := cl.Acquire(ctx, req, func(q wingwire.Queued) {
		reporter.onQueued(q)
		if onQueued != nil {
			onQueued(q)
		}
	})
	stopNarration()
	if err != nil {
		warnHostedClose("admission client", cl.Close())
		return nil, err
	}
	if reporter.waited() {
		h.la.reportGranted(ctx, h.backends, runID, node, req.RunID, reporter.displayID)
	}
	return hostedLease{lease}, nil
}

// safety: the node id comes from the request's own run id, which nodehost
// builds as <run>/node-host/<base64 node id>; a request in another shape is
// left for the daemon to price.
func (h *hostedAdmitter) profileCost(ctx context.Context, req *wingwire.AdmissionRequest) {
	_, encoded, ok := strings.Cut(req.RunID, "/node-host/")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if !ok || err != nil {
		return
	}
	node, key := string(raw), currentProfileKey(h.pipeline)
	prof, err := h.backends.State.GetPipelineProfile(ctx, key, node)
	if err != nil || !h.backends.LocalCoordination {
		prof = nil
	}
	fingerprint := ""
	if doc := h.plan(); doc != nil && prof != nil {
		fingerprint = hostedCapacityFingerprint(doc)
	}
	res := capacity.Resolve(nil, prof, runtime.NumCPU(), fingerprint)
	res, overCap := h.la.applyHostCeiling(ctx, res, key)
	req.Resources = wingwire.HostResources{Cores: res.Cores, MemoryBytes: res.MemoryBytes}
	req.CostSource, req.ExpectedDurationMS, req.DriftWarning = wingwire.CostSource(res.Source), res.ExpectedDuration.Milliseconds(), overCap
	if prof != nil {
		req.ExpectedP99MS, req.SampleCount = prof.P99Duration.Milliseconds(), prof.SampleCount
	}
}

type hostedLease struct{ l *wingdclient.Lease }

func (h hostedLease) Token() string   { return h.l.Token }
func (h hostedLease) HoldsHost() bool { return leaseCarriesHost(h.l) }
func (h hostedLease) Release() error  { return h.l.Release() }
func (h hostedLease) Watch(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel)) error {
	return h.l.WatchOwnership(onEvicted, onCancel, nil)
}

// safety: a nested run attaches to the lease of the node that started it, as the dispatcher's attachChildRun
// did, so it never queues behind the parent waiting on it; plan groups the parent lease lacks queue on their own.
func (h *hostedAdmitter) attach(ctx context.Context, req wingwire.AdmissionRequest) (nodehost.Lease, error) {
	cl, err := h.la.ensureDaemon(ctx)
	if err != nil {
		return nil, fmt.Errorf("local admission: %w", err)
	}
	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: req.RunID, Pipeline: req.Pipeline, Repo: currentRepoShortName(),
		ParentLeaseToken: h.la.ParentLeaseToken, ParentRunID: h.la.ParentRunID, Origin: h.la.Origin,
	}, nil)
	if err != nil {
		warnHostedClose("admission client", cl.Close())
		return nil, fmt.Errorf("local admission: attach to parent lease: %w", err)
	}
	h.attachToken, h.attachedHost = lease.Token, leaseCarriesHost(lease)
	var extra []wingwire.SemaphoreClaim
	for _, c := range req.Semaphores {
		if !slices.Contains(lease.Semaphores, c.Name) {
			extra = append(extra, c)
		}
	}
	if len(extra) == 0 {
		return hostedLease{lease}, nil
	}
	sems, err := h.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: req.RunID + localPlanSemsID, OwnerRunID: req.RunID, OwnerLeaseToken: lease.Token,
		DisplayRunID: req.RunID, SemaphoresOnly: true, Semaphores: extra, SubLease: true, Priority: req.Priority,
	}, nil)
	if err != nil {
		warnHostedClose("parent attach lease", lease.Release())
		return nil, err
	}
	return attachedLease{hostedLease{lease}, sems}, nil
}

type attachedLease struct {
	hostedLease
	sems nodehost.Lease
}

func (l attachedLease) Release() error {
	return errors.Join(l.sems.Release(), l.hostedLease.Release())
}

func (l attachedLease) Watch(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel)) error {
	done := make(chan error, 1)
	go func() { done <- l.sems.Watch(onEvicted, onCancel) }()
	err := l.hostedLease.Watch(onEvicted, onCancel)
	return errors.Join(err, <-done)
}

// safety: stands for a host lease the parent run already holds, so it ends only when released.
type heldLease struct {
	token string
	done  chan struct{}
	once  sync.Once
}

func (l *heldLease) Token() string { return l.token }

func (l *heldLease) Release() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *heldLease) Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error {
	<-l.done
	return nil
}
