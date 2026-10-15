package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
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

func localStatusResult(service, action string, status wingd.LocalStatus) dashboardResult {
	state := "stopped"
	if status.Running {
		state = "running"
	}
	return dashboardResult{
		Kind: "service", Tool: "sparkwing", Service: service, Action: action, State: state,
		PID: status.PID, Version: status.Version, Stay: status.Stay, Installed: status.Installed,
		Activity: dashboardActivity{IdleSince: status.Activity.IdleSince, Runs: status.Activity.Runs, QueuedTriggers: status.Activity.QueuedTriggers, HeldLeases: status.Activity.HeldLeases, DashboardConnections: status.Activity.DashboardConnections},
	}
}

func stopLocalController(ctx context.Context, lifecycle wingd.LocalLifecycle) (wingd.LocalStatus, error) {
	if err := ctx.Err(); err != nil {
		return wingd.LocalStatus{}, err
	}
	if err := lifecycle.Stop(ctx); err != nil {
		return wingd.LocalStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return wingd.LocalStatus{}, err
	}
	status, err := lifecycle.Status(ctx)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return wingd.LocalStatus{}, err
	}
	if status.Running {
		return wingd.LocalStatus{}, &apierr.Error{Code: apierr.Unavailable, Message: fmt.Sprintf("controller %s still answers after drain", status.Version), Hint: "run sparkwing doctor"}
	}
	return status, nil
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
