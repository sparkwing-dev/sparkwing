package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	Reserve(context.Context, store.ExecutorSchedulingSummary, store.ExecutorMembershipSnapshot, int) (ExecutorCapacityReservation, error)
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
	Consume() (*orchestrator.LocalAdmission, error)
	Release() error
}

// WingdExecutorCapacityLedger shares physical slots across memberships.
type WingdExecutorCapacityLedger struct {
	home, version string
	mu            sync.Mutex
	slots         map[string]*wingdExecutorReservation
	reservations  map[string]*wingdExecutorReservation
}

func NewWingdExecutorCapacityLedger(home, version string) *WingdExecutorCapacityLedger {
	return &WingdExecutorCapacityLedger{
		home: home, version: version,
		slots: map[string]*wingdExecutorReservation{}, reservations: map[string]*wingdExecutorReservation{},
	}
}

func (l *WingdExecutorCapacityLedger) Reserve(ctx context.Context, summary store.ExecutorSchedulingSummary, membership store.ExecutorMembershipSnapshot, slot int) (ExecutorCapacityReservation, error) {
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
	// safety: membership-agnostic keys stop two coordinators from sharing one physical slot
	key := fmt.Sprint(slot)
	l.mu.Lock()
	if _, occupied := l.slots[key]; occupied {
		l.mu.Unlock()
		return nil, ErrExecutorCapacityUnavailable
	}
	id, err := newExecutorReservationID()
	if err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if _, occupied := l.reservations[id]; occupied {
		l.mu.Unlock()
		return nil, errors.New("executor reservation: generated duplicate id")
	}
	r := &wingdExecutorReservation{
		id: id, membershipID: membership.MembershipID, workerID: membership.WorkerID,
		runID: summary.RunID, nodeID: summary.NodeID, resourceDigest: summary.ResourceDigest,
		slot: slot, ledger: l, key: key,
	}
	l.slots[key] = r
	l.reservations[id] = r
	l.mu.Unlock()
	cl, err := wingdclient.EnsureDaemon(ctx, wingdclient.Options{Home: l.home, Version: l.version})
	if err != nil {
		l.forget(r)
		return nil, fmt.Errorf("executor reservation: %w", err)
	}
	lease, err := cl.Acquire(ctx, wingwire.AdmissionRequest{
		RunID: "executor-reservation/" + id, DisplayRunID: summary.RunID + "/" + summary.NodeID,
		Resources:  wingwire.HostResources{Cores: summary.Resources.Cores, MemoryBytes: summary.Resources.MemoryBytes},
		CostSource: wingwire.CostSourcePin, Origin: wingwire.OriginController, NonBlocking: true,
	}, nil)
	if err != nil {
		_ = cl.Close()
		l.forget(r)
		return nil, fmt.Errorf("%w: %v", ErrExecutorCapacityUnavailable, err)
	}
	r.mu.Lock()
	r.lease = lease
	r.mu.Unlock()
	return r, nil
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
	consumed, released                                        bool
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

func (r *wingdExecutorReservation) Consume() (*orchestrator.LocalAdmission, error) {
	r.mu.Lock()
	if r.released || r.lease == nil {
		r.mu.Unlock()
		return nil, errors.New("executor reservation is not live")
	}
	if r.consumed {
		r.mu.Unlock()
		return nil, errors.New("executor reservation was already consumed")
	}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	state, err := wingdclient.Query(ctx, wingdclient.Options{Home: r.ledger.home, Version: r.ledger.version})
	cancel()
	if err != nil {
		return nil, fmt.Errorf("executor reservation is not live: %w", err)
	}
	live := false
	for _, holder := range state.Holders {
		if holder.RunID == "executor-reservation/"+r.id && holder.Origin == wingwire.OriginController {
			live = true
			break
		}
	}
	if !live {
		return nil, errors.New("executor reservation is not live")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released || r.lease == nil || r.consumed {
		return nil, errors.New("executor reservation is not live")
	}
	r.consumed = true
	return &orchestrator.LocalAdmission{Home: r.ledger.home, Version: r.ledger.version, ParentLeaseToken: r.lease.Token, Origin: wingwire.OriginController}, nil
}

func (r *wingdExecutorReservation) Release() error {
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return nil
	}
	r.released = true
	lease := r.lease
	r.mu.Unlock()
	r.ledger.forget(r)
	if lease != nil {
		return lease.Release()
	}
	return nil
}
