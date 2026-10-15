package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// LocalAdmission holds machine resources and local concurrency under sealed settings.
type LocalAdmission struct {
	Daemon Admitter
	BoxID  string
	Queued func(context.Context, store.NodeRef, wingwire.Queued)
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
	parent := localRunLease{runID: accepted.RunID, resources: resources.Cores > 0 || resources.MemoryBytes > 0}
	request := wingwire.AdmissionRequest{
		RunID: accepted.RunID, Pipeline: spec.Request.Request.Pipeline,
		Semaphores: claims, SemaphoresOnly: !parent.resources, Resources: resources,
		Priority: options.Priority.Value, Class: options.AdmissionClass.Value,
	}
	err = a.hold(ctx, store.NodeRef{RunID: accepted.RunID}, request, func(ctx context.Context, held Lease) error {
		parent.token = held.Token()
		completion, err = body(context.WithValue(ctx, localRunLease{}, parent))
		return err
	})
	return completion, err
}

// Node admits an unclaimed local job before release and holds its lease through cleanup.
func (a *LocalAdmission) Node(ctx context.Context, accepted AcceptedRun, row store.NodeRow, body func(context.Context) error) error {
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
	if parent.resources && len(claims) == 0 {
		return body(ctx)
	}
	options := spec.Resolved.Settings.Options
	request := wingwire.AdmissionRequest{
		OwnerRunID: accepted.RunID, OwnerLeaseToken: parent.token, SubLease: true,
		DisplayRunID: accepted.RunID + "/" + row.Ref.NodeID, Pipeline: spec.Request.Request.Pipeline,
		Semaphores: claims, SemaphoresOnly: parent.resources,
		Resources: localResources(row.Execution.Resources.Value), Priority: options.Priority.Value,
		Class: options.AdmissionClass.Value,
	}
	if parent.resources {
		request.RunID = wingwire.NodeSemaphoreParticipant(accepted.RunID, row.Ref.NodeID)
		request.Resources = wingwire.HostResources{}
	} else {
		request.RunID = wingwire.NodeHostParticipant(accepted.RunID, row.Ref.NodeID)
	}
	return a.hold(ctx, row.Ref, request, func(ctx context.Context, _ Lease) error { return body(ctx) })
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
	claims := make([]wingwire.SemaphoreClaim, 0, len(groups))
	for _, group := range groups {
		if group.Scope != "run" && group.Scope != "box" {
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

func (a *LocalAdmission) hold(parent context.Context, ref store.NodeRef, request wingwire.AdmissionRequest, body func(context.Context, Lease) error) (err error) {
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
		return errors.Join(context.Cause(ctx), err)
	}
	joined := make(chan error, 1)
	go func() {
		err := held.Watch(func(evicted wingwire.Evicted) {
			cancel(&lease.CancelError{Reason: fmt.Sprintf("admission on %q superseded by %s", evicted.Key, evicted.SupersededBy)})
		}, func(stopped wingwire.Cancel) {
			cancel(&lease.CancelError{Reason: stopped.Reason})
		})
		if err != nil {
			if errors.Is(err, wingdclient.ErrReattachRejected) {
				cancel(&lease.CancelError{Reason: "local admission lease lost"})
			} else {
				cancel(err)
			}
		}
		joined <- err
	}()
	defer func() {
		releaseErr := held.Release()
		watchErr := <-joined
		err = errors.Join(err, context.Cause(ctx), releaseErr, watchErr)
	}()
	if err == nil && context.Cause(ctx) == nil {
		err = body(ctx, held)
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
