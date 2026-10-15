package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type dashboardActivity struct {
	IdleSince            time.Time `json:"idle_since"`
	Runs                 int       `json:"runs"`
	QueuedTriggers       int       `json:"queued_triggers"`
	HeldLeases           int       `json:"held_leases"`
	DashboardConnections int       `json:"dashboard_connections"`
}

type dashboardResult struct {
	Kind      string            `json:"kind"`
	Tool      string            `json:"tool"`
	Service   string            `json:"service"`
	Action    string            `json:"action"`
	State     string            `json:"state"`
	PID       int               `json:"pid,omitempty"`
	Version   string            `json:"version,omitempty"`
	Stay      bool              `json:"stay"`
	Installed bool              `json:"installed"`
	Activity  dashboardActivity `json:"activity"`
}

func renderDashboard(out io.Writer, result dashboardResult, mode string) error {
	switch mode {
	case "json":
		return json.NewEncoder(out).Encode(result)
	case "plain":
		_, err := fmt.Fprintln(out, result.State)
		return err
	default:
		_, err := fmt.Fprintf(out, "Local controller: %s\npid: %d\nversion: %s\nstay: %t\ninstalled: %t\nactivity: %d runs, %d queued triggers, %d held leases, %d dashboard connections\n", result.State, result.PID, result.Version, result.Stay, result.Installed, result.Activity.Runs, result.Activity.QueuedTriggers, result.Activity.HeldLeases, result.Activity.DashboardConnections)
		return err
	}
}
