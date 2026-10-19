package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// Admitter acquires machine capacity under the local controller.
type Admitter interface {
	Acquire(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (Lease, error)
}

// Lease holds machine capacity until Release and joins ownership watching.
type Lease interface {
	Token() string
	Resources() wingwire.HostResources
	Release() error
	Watch(func(wingwire.Evicted), func(wingwire.Cancel)) error
}

var errQueueTimeout = errors.New("queue timeout")

// LocalAdmission holds machine resources and local concurrency under sealed settings.
type LocalAdmission struct {
	storeGroups bool
	Daemon      Admitter
	BoxID       string
	Controller  string
	Queued      func(context.Context, store.NodeRef, wingwire.Queued)
}

type localRunLease struct {
	runID, token string
	resources    bool
}

// Run keeps the parent admission lease until scheduling and every child have joined.
func (a *LocalAdmission) Run(ctx context.Context, accepted AcceptedRun, body func(context.Context) (RunCompletion, error)) (completion RunCompletion, err error) {
	spec, err := admissionSpec(accepted)
	if err != nil {
		return completion, err
	}
	options := spec.Resolved.Settings.Options
	claims, err := a.claims(spec.Resolved.Settings.Concurrency, accepted.RunID)
	if err != nil {
		return completion, err
	}
	resources := localResources(options.Resources.Value)
	if spec.Request.Request.Burst != nil {
		resources = wingwire.HostResources{}
	}
	parent := localRunLease{runID: accepted.RunID, resources: resources.Cores > 0 || resources.MemoryBytes > 0}
	request := wingwire.AdmissionRequest{
		RunID: wingwire.HolderRef(a.holderRunID(accepted.RunID)), Pipeline: spec.Request.Request.Pipeline,
		DisplayRunID: accepted.RunID,
		Semaphores:   claims, SemaphoresOnly: !parent.resources, Resources: resources,
		Priority: options.Priority.Value, Class: options.AdmissionClass.Value,
	}
	err = a.hold(ctx, store.NodeRef{RunID: accepted.RunID}, request, false, func(ctx context.Context, held Lease) error {
		parent.token = held.Token()
		completion, err = body(context.WithValue(ctx, localRunLease{}, parent))
		return err
	})
	return completion, err
}

// Node admits an unclaimed local job before release and holds its lease through cleanup.
func (a *LocalAdmission) Node(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
	return a.node(ctx, accepted, row, true, body)
}

// ClaimedNode holds machine capacity for one independently awarded job.
func (a *LocalAdmission) ClaimedNode(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	if row.Ref.RunID != accepted.RunID || row.Ref.NodeID == "" {
		return errors.New("claimed job admission requires its accepted run and job")
	}
	var claims []wingwire.SemaphoreClaim
	if spec.Request.Request.Burst == nil {
		claims, err = a.claims(row.Execution.Concurrency, accepted.RunID)
		if err != nil {
			return err
		}
	}
	options := spec.Resolved.Settings.Options
	request := wingwire.AdmissionRequest{
		RunID:        wingwire.NodeHostParticipant(a.holderRunID(accepted.RunID), row.Ref.NodeID),
		DisplayRunID: accepted.RunID + "/" + row.Ref.NodeID, Pipeline: spec.Request.Request.Pipeline,
		Semaphores: claims, Resources: localResources(row.Execution.Resources.Value),
		Priority: options.Priority.Value, Class: options.AdmissionClass.Value,
	}
	return a.hold(ctx, row.Ref, request, len(claims) == 0, func(ctx context.Context, _ Lease) error { return body(ctx) })
}

func (a *LocalAdmission) node(ctx context.Context, accepted AcceptedRun, row store.NodeRow, local bool, body func(context.Context) error) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	parent, ok := ctx.Value(localRunLease{}).(localRunLease)
	if !ok || parent.runID != accepted.RunID || parent.token == "" || row.Ref.RunID != accepted.RunID || row.Ref.NodeID == "" {
		return errors.New("local job admission requires its live parent run lease")
	}
	claims, err := a.claims(row.Execution.Concurrency, accepted.RunID)
	if err != nil {
		return err
	}
	semaphoresOnly := parent.resources || !local
	if semaphoresOnly && len(claims) == 0 {
		return body(ctx)
	}
	options := spec.Resolved.Settings.Options
	request := wingwire.AdmissionRequest{
		OwnerRunID: a.holderRunID(accepted.RunID), OwnerLeaseToken: parent.token, SubLease: true,
		DisplayRunID: accepted.RunID + "/" + row.Ref.NodeID, Pipeline: spec.Request.Request.Pipeline,
		Semaphores: claims, SemaphoresOnly: semaphoresOnly,
		Resources: localResources(row.Execution.Resources.Value), Priority: options.Priority.Value,
		Class: options.AdmissionClass.Value,
	}
	if semaphoresOnly {
		request.RunID = wingwire.NodeSemaphoreParticipant(a.holderRunID(accepted.RunID), row.Ref.NodeID)
		request.Resources = wingwire.HostResources{}
	} else {
		request.RunID = wingwire.NodeHostParticipant(a.holderRunID(accepted.RunID), row.Ref.NodeID)
	}
	return a.hold(ctx, row.Ref, request, !semaphoresOnly && len(claims) == 0, func(ctx context.Context, _ Lease) error { return body(ctx) })
}

func admissionSpec(accepted AcceptedRun) (runspec.Spec, error) {
	var spec runspec.Spec
	if accepted.RunID == "" || accepted.SpecHash == "" || accepted.PlanHash == "" || json.Unmarshal(accepted.Spec, &spec) != nil || string(spec.Hash) != accepted.SpecHash || string(spec.PlanHash) != accepted.PlanHash {
		return spec, errors.New("local admission requires the accepted run seal")
	}
	return spec, nil
}

func localResources(resources runspec.Resources) wingwire.HostResources {
	return wingwire.HostResources{Cores: resources.Cores.Value, MemoryBytes: resources.MemoryBytes.Value}
}

func (a *LocalAdmission) claims(groups []runspec.Concurrency, runID string) ([]wingwire.SemaphoreClaim, error) {
	runID = a.holderRunID(runID)
	claims := make([]wingwire.SemaphoreClaim, 0, len(groups))
	for _, group := range groups {
		if group.Scope != "run" && group.Scope != "box" {
			if a.storeGroups {
				continue
			}
			return nil, errors.New("local admission requires a store adapter for nonlocal concurrency")
		}
		if group.Scope == "box" && a.BoxID == "" {
			return nil, errors.New("box concurrency requires host machine identity")
		}
		claims = append(claims, wingwire.SemaphoreClaim{
			Name:     scopedConcurrencyKey(group.Group, group.Scope, runID, a.BoxID),
			Capacity: group.Capacity, Cost: group.Cost, Policy: wingwire.Policy(group.OnLimit),
			QueueTimeoutMS: value(group.QueueTimeoutMS), CancelTimeoutMS: value(group.CancelTimeoutMS),
		})
	}
	slices.SortFunc(claims, func(a, b wingwire.SemaphoreClaim) int { return strings.Compare(a.Name, b.Name) })
	return claims, nil
}

func (a *LocalAdmission) holderRunID(run string) string {
	if a.Controller == "" {
		return run
	}
	return string(wingwire.RemoteRunID(a.Controller, run))
}

// hold keeps request's lease for body. A yieldable lease is one job's machine
// capacity with no concurrency claims: body may hand it back while the job
// waits on a child run, which otherwise could never be admitted beside it.
func (a *LocalAdmission) hold(parent context.Context, ref store.NodeRef, request wingwire.AdmissionRequest, yieldable bool, body func(context.Context, Lease) error) (err error) {
	if err := context.Cause(parent); err != nil {
		return err
	}
	if a.Daemon == nil {
		return errors.New("local admission requires the controller admission client")
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	held, err := a.acquire(ctx, cancel, ref, request)
	if held == nil {
		if err == nil {
			err = errors.New("local admission returned no lease")
		}
		return localAdmissionFailure(errors.Join(context.Cause(ctx), err))
	}
	lease := &heldAdmission{admission: a, ref: ref, request: request, cancel: cancel}
	lease.attach(held)
	defer func() {
		err = errors.Join(err, localAdmissionFailure(context.Cause(ctx)), lease.close())
	}()
	if err == nil && context.Cause(ctx) == nil {
		bodyCtx := ctx
		if yieldable {
			bodyCtx = context.WithValue(ctx, heldAdmissionKey{}, lease)
		}
		err = body(bodyCtx, held)
	}
	return err
}

type heldAdmissionKey struct{}

// resumePriority puts a job returning from a child wait ahead of every new
// request, so the parent is not starved by work queued while it waited.
const resumePriority = math.MaxInt32

// heldAdmission is one job's lease, which it hands back while waiting on child runs.
type heldAdmission struct {
	admission *LocalAdmission
	ref       store.NodeRef
	request   wingwire.AdmissionRequest
	cancel    context.CancelCauseFunc

	mu      sync.Mutex
	held    Lease
	joined  chan error
	waiting int
	errs    []error
}

func (h *heldAdmission) attach(held Lease) {
	joined := make(chan error, 1)
	h.held, h.joined = held, joined
	go func() {
		err := held.Watch(func(evicted wingwire.Evicted) {
			h.lost(held, &AdmissionFailure{Outcome: store.NodeSuperseded, Err: &lease.CancelError{Reason: fmt.Sprintf("admission on %q superseded by %s", evicted.Key, evicted.SupersededBy)}})
		}, func(stopped wingwire.Cancel) {
			h.lost(held, &lease.CancelError{Reason: stopped.Reason})
		})
		if err != nil {
			if errors.Is(err, wingdclient.ErrReattachRejected) {
				h.lost(held, &AdmissionFailure{Outcome: store.NodeSuperseded, Err: &lease.CancelError{Reason: "local admission lease lost"}})
			} else {
				h.lost(held, err)
			}
		}
		joined <- err
	}()
}

// lost cancels the job only for the lease it still holds; a lease handed
// back for a child wait ends its watch without taking authority away.
func (h *heldAdmission) lost(held Lease, cause error) {
	h.mu.Lock()
	current := h.held == held
	h.mu.Unlock()
	if current {
		h.cancel(cause)
	}
}

// release hands back held outside h.mu, because its watch reports through lost.
func (h *heldAdmission) release() error {
	h.mu.Lock()
	held, joined := h.held, h.joined
	h.held, h.joined = nil, nil
	h.mu.Unlock()
	if held == nil {
		return nil
	}
	releaseErr := held.Release()
	return errors.Join(releaseErr, <-joined)
}

// wait hands the lease back until the returned resume, which takes it back
// ahead of new work. Concurrent waits share one release.
func (h *heldAdmission) wait() (resume func(context.Context)) {
	h.mu.Lock()
	h.waiting++
	first := h.waiting == 1
	h.mu.Unlock()
	if first {
		if err := h.release(); err != nil {
			h.mu.Lock()
			h.errs = append(h.errs, err)
			h.mu.Unlock()
		}
	}
	var once sync.Once
	return func(ctx context.Context) {
		once.Do(func() { h.resume(ctx) })
	}
}

func (h *heldAdmission) resume(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.waiting--
	if h.held != nil {
		return
	}
	request := h.request
	request.Priority = resumePriority
	held, err := h.admission.acquire(ctx, h.cancel, h.ref, request)
	// The daemon applies a release on its own connection, so the lease handed
	// back moments ago can still hold this participant's name.
	for attempt := 0; held == nil && releasePending(err) && attempt < resumeRetries; attempt++ {
		timer := time.NewTimer(resumeRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			err = context.Cause(ctx)
		case <-timer.C:
			held, err = h.admission.acquire(ctx, h.cancel, h.ref, request)
		}
	}
	if held == nil {
		if err == nil {
			err = errors.New("local admission returned no lease")
		}
		h.cancel(localAdmissionFailure(errors.Join(context.Cause(ctx), err)))
		return
	}
	h.attach(held)
}

const (
	resumeRetries    = 100
	resumeRetryDelay = 10 * time.Millisecond
)

func releasePending(err error) bool {
	var refused *wingdclient.AdmissionError
	return errors.As(err, &refused) && refused.Key == "duplicate"
}

func (h *heldAdmission) close() error {
	err := h.release()
	h.mu.Lock()
	defer h.mu.Unlock()
	return errors.Join(append(h.errs, err)...)
}

// awaitChildAdmission hands a yieldable job lease back while ctx's job waits on a child run.
func awaitChildAdmission(ctx context.Context) func() {
	held, ok := ctx.Value(heldAdmissionKey{}).(*heldAdmission)
	if !ok {
		return func() {}
	}
	resume := held.wait()
	return func() { resume(ctx) }
}

func localAdmissionFailure(err error) error {
	if errors.Is(err, errQueueTimeout) {
		return errors.Join(&AdmissionFailure{Outcome: store.NodeFailed, Reason: store.FailureQueueTimeout, Err: err}, waitFailure(err, errQueueTimeout))
	}
	var refused *wingdclient.AdmissionError
	if errors.As(err, &refused) {
		switch refused.Policy {
		case wingwire.PolicySkip:
			return errors.Join(&AdmissionFailure{Outcome: store.NodeSkipped, Err: err}, waitFailure(err, refused))
		case wingwire.PolicyFail:
			return errors.Join(&AdmissionFailure{Outcome: store.NodeFailed, Err: err}, waitFailure(err, refused))
		}
	}
	return err
}

func (a *LocalAdmission) acquire(ctx context.Context, cancel context.CancelCauseFunc, ref store.NodeRef, request wingwire.AdmissionRequest) (Lease, error) {
	var mu sync.Mutex
	var timer *time.Timer
	waiting := ""
	acquiring := true
	onQueued := func(queued wingwire.Queued) {
		mu.Lock()
		if waiting != queued.Key {
			if timer != nil {
				timer.Stop()
				timer = nil
			}
			waiting = queued.Key
			for _, claim := range request.Semaphores {
				if claim.Name == queued.Key && claim.QueueTimeoutMS > 0 && claim.Policy == wingwire.PolicyQueue {
					timer = time.AfterFunc(time.Duration(claim.QueueTimeoutMS)*time.Millisecond, func() {
						mu.Lock()
						defer mu.Unlock()
						if acquiring && waiting == claim.Name {
							cancel(errQueueTimeout)
						}
					})
				}
			}
		}
		mu.Unlock()
		if a.Queued != nil {
			a.Queued(ctx, ref, queued)
		}
	}
	held, err := a.Daemon.Acquire(ctx, request, onQueued)
	mu.Lock()
	acquiring = false
	if timer != nil {
		timer.Stop()
	}
	mu.Unlock()
	return held, err
}
