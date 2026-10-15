package wingd

import "github.com/sparkwing-dev/sparkwing/pkg/wingwire"

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
