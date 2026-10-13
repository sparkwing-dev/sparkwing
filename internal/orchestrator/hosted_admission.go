package orchestrator

import (
	"cmp"
	"context"
	"encoding/base64"
	"runtime"
	"strings"

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
}

func (h *hostedAdmitter) Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (nodehost.Lease, error) {
	if !req.SemaphoresOnly && req.Resources == (wingwire.HostResources{}) {
		h.profileCost(ctx, &req)
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

func (h hostedLease) Token() string  { return h.l.Token }
func (h hostedLease) Release() error { return h.l.Release() }
func (h hostedLease) Watch(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel)) error {
	return h.l.WatchOwnership(onEvicted, onCancel, nil)
}
