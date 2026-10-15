package wingwire

import "time"

// LocalControl observes the elected controller, or changes its stay policy when
// Stay is present. Health-probe connections may only observe. Neither operation
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
}

func (*LocalControl) wireType() MessageType       { return TypeLocalControl }
func (*LocalControlStatus) wireType() MessageType { return TypeLocalControlStatus }
