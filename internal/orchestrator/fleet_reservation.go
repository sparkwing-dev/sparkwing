package orchestrator

import (
	"context"
	"errors"
	"sync/atomic"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// FleetNodeReservation holds host capacity before a remote claim offer. Its ID
// is safe to send to a coordinator; the wingd lease token remains local.
type FleetNodeReservation struct {
	id    string
	lease *wingdclient.Lease
}

func (r *FleetNodeReservation) ID() string {
	if r == nil {
		return ""
	}
	return r.id
}

func (r *FleetNodeReservation) Release() {
	if r != nil && r.lease != nil {
		_ = r.lease.Release()
	}
}

func (r *FleetNodeReservation) Watch(cancel context.CancelFunc) {
	if r == nil || r.lease == nil {
		return
	}
	r.lease.Watch(func(wingwire.Evicted) { cancel() })
}

// TryReserveFleetNode acquires host capacity immediately or reports false as
// soon as wingd would queue it.
func (la *LocalAdmission) TryReserveFleetNode(
	ctx context.Context,
	summary store.NodeSchedulingSummary,
	reservationID string,
) (*FleetNodeReservation, bool, error) {
	cl, err := la.ensureDaemon(ctx)
	if err != nil {
		return nil, false, err
	}
	reserveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var queued atomic.Bool
	lease, err := cl.Acquire(reserveCtx, wingwire.AdmissionRequest{
		RunID:        "fleet-offer:" + reservationID,
		OwnerRunID:   summary.RunID,
		DisplayRunID: summary.RunID + "/" + summary.NodeID,
		PID:          0,
		Resources: wingwire.HostResources{
			Cores:       summary.RequestedCores,
			MemoryBytes: summary.RequestedMemoryBytes,
		},
		Origin:   wingwire.OriginController,
		SubLease: true,
	}, func(wingwire.Queued) {
		queued.Store(true)
		cancel()
	})
	if queued.Load() {
		if lease != nil {
			_ = lease.Release()
		} else {
			_ = cl.Close()
		}
		return nil, false, nil
	}
	if err != nil {
		_ = cl.Close()
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &FleetNodeReservation{id: reservationID, lease: lease}, true, nil
}
