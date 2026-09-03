package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// ErrExecutorCapacityUnavailable means wingd or a physical slot could not
// grant the helper reservation immediately.
var ErrExecutorCapacityUnavailable = errors.New("executor capacity is not immediately available")

// ExecutorCapacityLedger reserves real helper capacity before an offer.
type ExecutorCapacityLedger interface {
	Reserve(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, executorCapacityLimits, int) (ExecutorCapacityReservation, error)
}

type executorCapacityLimits struct {
	localReserve, globalContribution, membershipContribution reserve
}

// ExecutorCapacityReservation is one single-use admission lease. Release is
// idempotent and remains required after Consume when execution finishes.
type ExecutorCapacityReservation interface {
	ID() string
	MembershipID() string
	WorkerID() string
	RunID() string
	NodeID() string
	ResourceDigest() string
	Slot() int
	ExecutionContext(context.Context) (context.Context, context.CancelFunc)
	Consume() (*orchestrator.LocalAdmission, error)
	Release() error
}

// WingdExecutorCapacityLedger shares physical slots across memberships.
type WingdExecutorCapacityLedger struct {
	home, version string
	mu            sync.Mutex
	slots         map[string]*wingdExecutorReservation
	reservations  map[string]*wingdExecutorReservation
	logger        *slog.Logger
	spawn         func(home, version string) error
}

func NewWingdExecutorCapacityLedger(home, version string, logger ...*slog.Logger) *WingdExecutorCapacityLedger {
	var log *slog.Logger
	if len(logger) > 0 {
		log = logger[0]
	}
	return &WingdExecutorCapacityLedger{
		home: home, version: version,
		slots: map[string]*wingdExecutorReservation{}, reservations: map[string]*wingdExecutorReservation{}, logger: log,
	}
}

func (l *WingdExecutorCapacityLedger) clientOptions() wingdclient.Options {
	return wingdclient.Options{Home: l.home, Version: l.version, Spawn: l.spawn}
}

func (l *WingdExecutorCapacityLedger) Reserve(ctx context.Context, summary store.ExecutorSchedulingSummary, membership store.ExecutorMembershipSnapshot, limits executorCapacityLimits, slot int) (ExecutorCapacityReservation, error) {
	if !membership.Eligible {
		return nil, ErrExecutorCapacityUnavailable
	}
	if summary.RunID == "" || summary.NodeID == "" || summary.ResourceDigest == "" ||
		membership.MembershipID == "" || membership.WorkerID == "" ||
		summary.Slots != 1 || slot < 0 || slot >= membership.MaxConcurrent {
		return nil, fmt.Errorf("executor reservation: invalid identity, summary, or slot %d", slot)
	}
	if summary.Resources.Cores < 0 || math.IsNaN(summary.Resources.Cores) || math.IsInf(summary.Resources.Cores, 0) || summary.Resources.MemoryBytes < 0 {
		return nil, errors.New("executor reservation: resources must be non-negative")
	}
	id, err := newExecutorReservationID()
	if err != nil {
		return nil, err
	}
	cl, err := wingdclient.EnsureDaemon(ctx, l.clientOptions())
	if err != nil {
		return nil, fmt.Errorf("executor reservation: %w", err)
	}
	// safety: daemon semaphores cover processes; the mutex closes the local slot check/acquire race.
	key := fmt.Sprint(slot)
	l.mu.Lock()
	if _, occupied := l.slots[key]; occupied {
		l.mu.Unlock()
		_ = cl.Close()
		return nil, ErrExecutorCapacityUnavailable
	}
	if _, occupied := l.reservations[id]; occupied {
		l.mu.Unlock()
		_ = cl.Close()
		return nil, errors.New("executor reservation: generated duplicate id")
	}
	queue, err := cl.QueueState(ctx)
	if err != nil {
		l.mu.Unlock()
		_ = cl.Close()
		return nil, fmt.Errorf("executor reservation: %w", err)
	}
	semaphores, ok := executorCapacitySemaphores(queue, summary.Resources, membership.MembershipID, limits, slot)
	if !ok {
		l.mu.Unlock()
		_ = cl.Close()
		return nil, ErrExecutorCapacityUnavailable
	}
	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: "executor-reservation/" + id, DisplayRunID: summary.RunID + "/" + summary.NodeID,
		Resources:  wingwire.HostResources{Cores: summary.Resources.Cores, MemoryBytes: summary.Resources.MemoryBytes},
		Semaphores: semaphores, CostSource: wingwire.CostSourcePin, Origin: wingwire.OriginController, NonBlocking: true,
	}, nil)
	if err != nil {
		l.mu.Unlock()
		_ = cl.Close()
		return nil, fmt.Errorf("%w: %v", ErrExecutorCapacityUnavailable, err)
	}
	r := &wingdExecutorReservation{
		id: id, membershipID: membership.MembershipID, workerID: membership.WorkerID,
		runID: summary.RunID, nodeID: summary.NodeID, resourceDigest: summary.ResourceDigest,
		slot: slot, ledger: l, key: key,
	}
	r.mu.Lock()
	r.lease = lease
	r.ownershipCtx, r.cancelOwnership = context.WithCancelCause(context.Background())
	r.watchDone = make(chan struct{})
	r.mu.Unlock()
	l.slots[key] = r
	l.reservations[id] = r
	l.mu.Unlock()
	r.log("executor reservation acquired")
	go r.watchLease(lease)
	return r, nil
}

const executorCapacityMemoryUnit = int64(1 << 20)

func executorCapacitySemaphores(queue wingwire.QueueState, resources store.ExecutorResource, membershipID string, limits executorCapacityLimits, slot int) ([]wingwire.SemaphoreClaim, bool) {
	var machineCores float64
	var machineMemory int64
	for _, resource := range queue.Resources {
		switch resource.Key {
		case "cores":
			machineCores = resource.Capacity
		case "memory":
			machineMemory = int64(resource.Capacity)
		}
	}
	global, membership := resolvedContributionBudgets(
		machineCores, machineMemory, limits.localReserve, limits.globalContribution, limits.membershipContribution,
	)
	claims := []wingwire.SemaphoreClaim{{
		Name: fmt.Sprintf("sparkwing/executor/slot/%d", slot), Cost: 1, Capacity: 1, Policy: wingwire.PolicyFail,
	}}
	coreCost := int(math.Ceil(resources.Cores * 1000))
	if coreCost > 0 {
		globalCapacity := int(math.Floor(global.Cores * 1000))
		membershipCapacity := int(math.Floor(membership.Cores * 1000))
		if coreCost > globalCapacity || coreCost > membershipCapacity {
			return nil, false
		}
		claims = append(claims,
			wingwire.SemaphoreClaim{Name: "sparkwing/executor/global/cores", Cost: coreCost, Capacity: globalCapacity, Policy: wingwire.PolicyFail},
			wingwire.SemaphoreClaim{Name: "sparkwing/executor/membership/" + membershipID + "/cores", Cost: coreCost, Capacity: membershipCapacity, Policy: wingwire.PolicyFail},
		)
	}
	memoryCost := memorySemaphoreUnits(resources.MemoryBytes, true)
	if memoryCost > 0 {
		globalCapacity := memorySemaphoreUnits(global.MemoryBytes, false)
		membershipCapacity := memorySemaphoreUnits(membership.MemoryBytes, false)
		if memoryCost > globalCapacity || memoryCost > membershipCapacity {
			return nil, false
		}
		claims = append(claims,
			wingwire.SemaphoreClaim{Name: "sparkwing/executor/global/memory", Cost: memoryCost, Capacity: globalCapacity, Policy: wingwire.PolicyFail},
			wingwire.SemaphoreClaim{Name: "sparkwing/executor/membership/" + membershipID + "/memory", Cost: memoryCost, Capacity: membershipCapacity, Policy: wingwire.PolicyFail},
		)
	}
	return claims, true
}

func memorySemaphoreUnits(bytes int64, roundUp bool) int {
	if bytes <= 0 {
		return 0
	}
	if roundUp {
		return int((bytes-1)/executorCapacityMemoryUnit + 1)
	}
	return int(bytes / executorCapacityMemoryUnit)
}

func (l *WingdExecutorCapacityLedger) forget(r *wingdExecutorReservation) {
	l.mu.Lock()
	if l.slots[r.key] == r {
		delete(l.slots, r.key)
	}
	if l.reservations[r.id] == r {
		delete(l.reservations, r.id)
	}
	l.mu.Unlock()
}

func newExecutorReservationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("executor reservation id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

type wingdExecutorReservation struct {
	id, membershipID, workerID, runID, nodeID, resourceDigest string
	slot                                                      int
	ledger                                                    *WingdExecutorCapacityLedger
	key                                                       string
	mu                                                        sync.Mutex
	lease                                                     *wingdclient.Lease
	ownershipCtx                                              context.Context
	cancelOwnership                                           context.CancelCauseFunc
	watchDone                                                 chan struct{}
	consumed, released, lost                                  bool
}

func (r *wingdExecutorReservation) ID() string           { return r.id }
func (r *wingdExecutorReservation) MembershipID() string { return r.membershipID }
func (r *wingdExecutorReservation) WorkerID() string     { return r.workerID }
func (r *wingdExecutorReservation) RunID() string        { return r.runID }
func (r *wingdExecutorReservation) NodeID() string       { return r.nodeID }
func (r *wingdExecutorReservation) ResourceDigest() string {
	return r.resourceDigest
}
func (r *wingdExecutorReservation) Slot() int { return r.slot }

func (r *wingdExecutorReservation) ExecutionContext(parent context.Context) (context.Context, context.CancelFunc) {
	r.mu.Lock()
	ownership := r.ownershipCtx
	r.mu.Unlock()
	ctx, cancel := context.WithCancelCause(parent)
	if ownership == nil {
		cancel(errors.New("executor reservation ownership is unavailable"))
		return ctx, func() {}
	}
	stop := context.AfterFunc(ownership, func() {
		cancel(context.Cause(ownership))
	})
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}

func (r *wingdExecutorReservation) watchLease(lease *wingdclient.Lease) {
	defer close(r.watchDone)
	err := lease.WatchOwnership(
		func(ev wingwire.Evicted) {
			r.loseOwnership(fmt.Errorf("executor reservation was evicted: %s", ev.Reason))
		},
		func(cancel wingwire.Cancel) {
			r.loseOwnership(fmt.Errorf("executor reservation was cancelled: %s", cancel.Reason))
		},
		func() { r.log("executor reservation reattached") },
	)
	if err == nil {
		err = errors.New("executor reservation ownership watch stopped")
	}
	r.loseOwnership(err)
}

func (r *wingdExecutorReservation) loseOwnership(err error) {
	r.mu.Lock()
	if r.released || r.lost {
		r.mu.Unlock()
		return
	}
	r.lost = true
	cancel := r.cancelOwnership
	r.mu.Unlock()
	if cancel != nil {
		cancel(fmt.Errorf("executor reservation ownership lost: %w", err))
	}
	r.log("executor reservation lost", "err", err)
}

func (r *wingdExecutorReservation) log(message string, attrs ...any) {
	if r.ledger.logger == nil {
		return
	}
	base := []any{
		"executor", r.workerID,
		"run_id", r.runID,
		"node_id", r.nodeID,
		"slot", r.slot,
	}
	r.ledger.logger.Info(message, append(base, attrs...)...)
}

func (r *wingdExecutorReservation) Consume() (*orchestrator.LocalAdmission, error) {
	r.mu.Lock()
	if r.released || r.lost || r.lease == nil {
		r.mu.Unlock()
		return nil, errors.New("executor reservation is not live")
	}
	if r.consumed {
		r.mu.Unlock()
		return nil, errors.New("executor reservation was already consumed")
	}
	defer r.mu.Unlock()
	r.consumed = true
	return orchestrator.NewReservedNodeAdmission(
		r.ledger.home, r.ledger.version, r.lease.Token, wingwire.OriginController,
	), nil
}

func (r *wingdExecutorReservation) Release() error {
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return nil
	}
	r.released = true
	lease := r.lease
	cancelOwnership := r.cancelOwnership
	watchDone := r.watchDone
	r.mu.Unlock()
	if cancelOwnership != nil {
		cancelOwnership(errors.New("executor reservation released"))
	}
	r.ledger.forget(r)
	var releaseErr error
	if lease != nil {
		releaseErr = lease.Release()
	}
	if watchDone != nil {
		select {
		case <-watchDone:
		case <-time.After(2 * time.Second):
			return errors.Join(releaseErr, errors.New("executor reservation ownership watch did not stop"))
		}
	}
	r.log("executor reservation released", "err", releaseErr)
	return releaseErr
}
