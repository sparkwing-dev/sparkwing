package wingd

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func (d *Daemon) handleLocalControl(c *conn, request *wingwire.LocalControl) bool {
	if d.localControl == nil {
		return c.refuse(wingwire.TypeOf(request))
	}
	status := d.localControl(request.Stay)
	return c.send(&wingwire.LocalControlStatus{
		Running: status.Running, Stay: status.Stay, PID: status.PID, Version: status.Version,
		Runs: status.Activity.Runs, QueuedTriggers: status.Activity.QueuedTriggers,
		HeldLeases: status.Activity.HeldLeases, DashboardConnections: status.Activity.DashboardConnections,
		IdleSince: status.Activity.IdleSince,
	}) != nil
}

func (d *Daemon) handleLocalDashboard(c *conn, request *wingwire.LocalDashboard) bool {
	if d.localDashboard == nil {
		return c.refuse(wingwire.TypeOf(request))
	}
	ctx, cancel := context.WithCancel(context.Background())
	peerDone := make(chan struct{})
	// safety: a disconnected peer or daemon shutdown must interrupt readiness before connection joins.
	go func() {
		defer close(peerDone)
		_, _ = c.readMessage()
		cancel()
	}()
	defer func() {
		cancel()
		c.close()
		<-peerDone
	}()
	reply := &wingwire.LocalDashboardAck{}
	if err := d.localDashboard(ctx, request.Address); err != nil {
		reply.Error = err.Error()
	}
	if ctx.Err() == nil {
		_ = c.send(reply)
	}
	return true
}
