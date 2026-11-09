package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type dashboardActivity struct {
	Runs                 int `json:"runs"`
	QueuedTriggers       int `json:"queued_triggers"`
	HeldLeases           int `json:"held_leases"`
	DashboardConnections int `json:"dashboard_connections"`
}

type dashboardResult struct {
	Kind           string            `json:"kind"`
	Tool           string            `json:"tool"`
	Service        string            `json:"service"`
	Action         string            `json:"action"`
	State          string            `json:"state"`
	PID            int               `json:"pid,omitempty"`
	Version        string            `json:"version,omitempty"`
	Installed      bool              `json:"installed"`
	Dashboard      string            `json:"dashboard"`
	URL            string            `json:"url,omitempty"`
	DashboardError string            `json:"dashboard_error,omitempty"`
	Activity       dashboardActivity `json:"activity"`
	Warning        string            `json:"warning,omitempty"`
}

func localStatusResult(service, action string, status wingd.LocalStatus) dashboardResult {
	state, dashboard := "stopped", "not_serving"
	if status.Running {
		state = "running"
	}
	if status.Running && status.DashboardURL != "" {
		dashboard = "serving"
	}
	return dashboardResult{
		Kind: "service", Tool: "sparkwing", Service: service, Action: action, State: state, Dashboard: dashboard,
		PID: status.PID, Version: status.Version, Installed: status.Installed, URL: status.DashboardURL, DashboardError: status.DashboardError,
		Activity: dashboardActivity{Runs: status.Activity.Runs, QueuedTriggers: status.Activity.QueuedTriggers, HeldLeases: status.Activity.HeldLeases, DashboardConnections: status.Activity.DashboardConnections},
	}
}

// safety: v0.67 and earlier ran the dashboard as its own process recorded in this file, which v0.68 never writes.
const olderDashboardPIDFile = "dashboard.pid"

func processRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func olderDashboardWarning(home string) string {
	path := filepath.Join(home, olderDashboardPIDFile)
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid <= 0 || !processRunning(pid) {
		return ""
	}
	return fmt.Sprintf("a dashboard from Sparkwing v0.67 or earlier recorded pid %d in %s, and a process with that pid is running; this command does not stop it. "+
		"If `ps -p %d` shows sparkwing, stop it with `kill %d` and delete %s before moving an old store aside", pid, path, pid, pid, path)
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
		return wingd.LocalStatus{}, &apierr.Error{Code: hosterr.Unavailable, Message: fmt.Sprintf("controller %s still answers after drain", status.Version), Hint: "run sparkwing doctor"}
	}
	return status, nil
}

func renderDashboard(out io.Writer, result dashboardResult, mode string) error {
	switch mode {
	case "json":
		return json.NewEncoder(out).Encode(result)
	case "plain":
		if result.Warning != "" {
			fmt.Fprintln(os.Stderr, "warning: "+result.Warning)
		}
		_, err := fmt.Fprintln(out, result.State)
		return err
	default:
		_, err := fmt.Fprintf(out, "Local controller: %s\npid: %d\nversion: %s\ninstalled: %t\n", result.State, result.PID, result.Version, result.Installed)
		if err == nil {
			switch {
			case result.Dashboard == "serving":
				_, err = fmt.Fprintf(out, "dashboard: serving at %s (sign in with sparkwing dashboard open)\n", result.URL)
			case result.DashboardError != "":
				_, err = fmt.Fprintf(out, "dashboard: not serving: %s\n", result.DashboardError)
			default:
				_, err = fmt.Fprintln(out, "dashboard: not serving (sparkwing dashboard open starts the controller and prints a sign-in link)")
			}
		}
		if err == nil {
			_, err = fmt.Fprintf(out, "activity: %d runs, %d queued triggers, %d held leases, %d dashboard connections\n", result.Activity.Runs, result.Activity.QueuedTriggers, result.Activity.HeldLeases, result.Activity.DashboardConnections)
		}
		if err == nil && result.Warning != "" {
			_, err = fmt.Fprintf(out, "warning: %s\n", result.Warning)
		}
		return err
	}
}

type dashboardLoginResult struct {
	Kind      string    `json:"kind"`
	Tool      string    `json:"tool"`
	Service   string    `json:"service"`
	Action    string    `json:"action"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func openDashboard(ctx context.Context, services dashboardServices, host string, out io.Writer, mode string) error {
	if services.mintLogin == nil {
		return dashboardUnavailable("local controller sign-in links")
	}
	if services.lifecycle == nil {
		return dashboardUnavailable("local controller lifecycle")
	}
	status, err := services.lifecycle.EnsureStarted(ctx, services.options)
	if err != nil {
		return err
	}
	if status.DashboardError != "" {
		return &apierr.Error{
			Code: hosterr.Unavailable, Message: "the local controller serves no dashboard: " + status.DashboardError,
			Hint: "free the port or set dashboard.address in config.yaml, then run sparkwing daemon restart --force",
		}
	}
	login, err := services.mintLogin(ctx, host)
	if err != nil {
		return err
	}
	switch mode {
	case "json":
		return json.NewEncoder(out).Encode(dashboardLoginResult{Kind: "service", Tool: "sparkwing", Service: "dashboard", Action: "open", URL: login.URL, ExpiresAt: login.ExpiresAt})
	case "plain":
		_, err := fmt.Fprintln(out, login.URL)
		return err
	default:
		_, err := fmt.Fprintf(out, "Open this link to sign in to the local dashboard. It works once and expires at %s.\n%s\n", login.ExpiresAt.Local().Format(time.TimeOnly), login.URL)
		return err
	}
}
