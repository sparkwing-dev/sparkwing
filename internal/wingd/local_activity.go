package wingd

import "fmt"

func (d *Daemon) dashboardConnection(delta int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dashboardConns += delta
}

func (d *Daemon) setLocalActivity(activity LocalActivity) error {
	if activity.Runs < 0 || activity.QueuedTriggers < 0 || activity.HeldLeases < 0 || activity.DashboardConnections < 0 {
		return fmt.Errorf("wingd: local activity counts must be nonnegative")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.localActivity = activity
	return nil
}
