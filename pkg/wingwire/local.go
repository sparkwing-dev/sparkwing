package wingwire

// LocalControl observes the elected controller through a verified local peer.
// It starts no controller and opens no store.
type LocalControl struct{}

// LocalControlStatus reports process readiness after reconciliation and listener
// startup. Service installation is a client-side OS fact.
type LocalControlStatus struct {
	Running              bool   `json:"running"`
	PID                  int    `json:"pid"`
	Version              string `json:"version"`
	Runs                 int    `json:"runs"`
	QueuedTriggers       int    `json:"queued_triggers"`
	HeldLeases           int    `json:"held_leases"`
	DashboardConnections int    `json:"dashboard_connections"`
	// DashboardURL is the dashboard origin while it serves, never a sign-in link.
	DashboardURL string `json:"dashboard_url,omitempty"`
}
