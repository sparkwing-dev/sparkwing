package wingwire

import "time"

// LocalControl observes the elected controller, or changes its stay policy when
// Stay is present. Both operations require a verified local peer. Neither operation
// starts a controller or opens its store.
type LocalControl struct {
	Stay *bool `json:"stay,omitempty"`
}

// LocalControlStatus reports process readiness after reconciliation and listener
// startup. A stay change is acknowledged only when Running is true and Stay
// matches the request. Service installation is a client-side OS fact.
type LocalControlStatus struct {
	Running              bool      `json:"running"`
	Stay                 bool      `json:"stay"`
	PID                  int       `json:"pid"`
	Version              string    `json:"version"`
	Runs                 int       `json:"runs"`
	QueuedTriggers       int       `json:"queued_triggers"`
	HeldLeases           int       `json:"held_leases"`
	DashboardConnections int       `json:"dashboard_connections"`
	IdleSince            time.Time `json:"idle_since"`
	// DashboardURL is the dashboard origin while it serves, never a sign-in link.
	DashboardURL string `json:"dashboard_url,omitempty"`
}

// LocalDashboard starts the dashboard on the elected owner and waits for its
// serving loop. Cancelling the HTTP request cancels the wait.
// Health probes and admission-only daemons cannot serve this operation.
type LocalDashboard struct {
	Address  string `json:"address"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// LocalDashboardAck acknowledges readiness, or names a failed start. A
// failed transport is an error even if no result arrived. Retrying an identical
// address and read-only policy reuse the owner's listener; it never spawns another controller.
type LocalDashboardAck struct {
	ReadOnly bool   `json:"read_only,omitempty"`
	Error    string `json:"error,omitempty"`
}
